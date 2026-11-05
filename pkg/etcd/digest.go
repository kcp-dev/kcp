/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package etcd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apiserver/pkg/storage/value"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
)

// LeaseTimeToLive abstracts etcd's lease TTL lookup.
type LeaseTimeToLive interface {
	TimeToLive(ctx context.Context, id clientv3.LeaseID, opts ...clientv3.LeaseOption) (*clientv3.LeaseTimeToLiveResponse, error)
}

// ScanEtcdEntries returns a single page of etcd key/value pairs belonging
// to the given logical cluster, resuming at continueToken if set.
//
// If endKey is non-empty, the scan stops before reaching endKey (exclusive).
// The page stops growing once it holds limit entries or once adding another
// entry would push the accumulated value size past maxBytes, whichever
// happens first (a single entry larger than maxBytes is still returned
// alone, so the scan always makes progress). If limit or maxBytes is zero
// or negative, a default is used.
//
// The second return value is the continue token for the next page, or the
// empty string if the scan reached the end of the logical cluster's
// keyspace (or endKey).
func ScanEtcdEntries(
	ctx context.Context,
	kv clientv3.KV,
	leases LeaseTimeToLive,
	storagePrefix string,
	target logicalcluster.Name,
	continueToken, endKey string,
	limit, maxBytes int64,
	transformers value.ResourceTransformers,
) ([]migrationv1alpha1.EtcdEntry, string, error) {
	prefix := storagePrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if limit <= 0 {
		limit = ScanPageSize
	}
	if maxBytes <= 0 {
		maxBytes = DumpMaxBytes
	}

	key := prefix
	if continueToken != "" {
		key = prefix + strings.TrimPrefix(continueToken, "/")
	}

	var stopKey string
	if endKey != "" {
		stopKey = prefix + strings.TrimPrefix(endKey, "/")
	}

	// Cap the raw etcd scan chunk size at the caller's requested page
	// limit (but never above ScanPageSize) so a small page request
	// doesn't pull more keys from etcd than it can possibly use.
	etcdScanLimit := limit
	if etcdScanLimit > ScanPageSize {
		etcdScanLimit = ScanPageSize
	}

	var entries []migrationv1alpha1.EtcdEntry
	var totalBytes int64
	ttls := map[int64]int64{}
	for {
		if stopKey != "" && key >= stopKey {
			return entries, "", nil
		}
		clusterPrefix, start, err := NextClusterRange(ctx, kv, prefix, key, target)
		if err != nil {
			return nil, "", err
		}
		if clusterPrefix == "" || (stopKey != "" && start >= stopKey) {
			return entries, "", nil
		}
		key = start
		rangeEnd := clientv3.GetPrefixRangeEnd(clusterPrefix)
		if stopKey != "" && stopKey < rangeEnd {
			rangeEnd = stopKey
		}
		for {
			resp, err := kv.Get(ctx, key,
				clientv3.WithRange(rangeEnd),
				clientv3.WithLimit(etcdScanLimit),
			)
			if err != nil {
				return nil, "", fmt.Errorf("failed to list etcd keys: %w", err)
			}

			for _, entry := range resp.Kvs {
				if err := ctx.Err(); err != nil {
					return nil, "", err
				}
				if stopKey != "" && string(entry.Key) >= stopKey {
					return entries, "", nil
				}
				if !BelongsToCluster(prefix, string(entry.Key), target) {
					continue
				}
				if int64(len(entries)) >= limit {
					return entries, strings.TrimPrefix(string(entry.Key), prefix), nil
				}

				var ttl int64
				if entry.Lease != 0 && leases != nil {
					var ok bool
					if ttl, ok = ttls[entry.Lease]; !ok {
						ttlResp, err := leases.TimeToLive(ctx, clientv3.LeaseID(entry.Lease))
						if err != nil {
							return nil, "", fmt.Errorf("failed to get lease of etcd key %q: %w", entry.Key, err)
						}
						ttl = ttlResp.TTL
						ttls[entry.Lease] = ttl
					}
					// The lease has expired (etcd reports -1) and etcd is about to
					// delete the key, so don't resurrect it on the destination.
					if ttl <= 0 {
						continue
					}
				}

				relativeKey := strings.TrimPrefix(string(entry.Key), prefix)
				transformer, err := TransformerForKey(transformers, relativeKey)
				if err != nil {
					return nil, "", err
				}
				data, _, err := transformer.TransformFromStorage(ctx, entry.Value, value.DefaultContext(entry.Key))
				if err != nil {
					return nil, "", fmt.Errorf("failed to transform source key %q: %w", entry.Key, err)
				}
				if len(entries) > 0 && totalBytes+int64(len(data)) > maxBytes {
					return entries, relativeKey, nil
				}
				entries = append(entries, migrationv1alpha1.EtcdEntry{
					Key:        relativeKey,
					Value:      data,
					TTLSeconds: ttl,
				})
				totalBytes += int64(len(data))
			}

			if !resp.More {
				key = rangeEnd
				break
			}
			key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
		}
	}
}

// ScanEtcdDigests returns a single page of etcd key/SHA-256-digest pairs
// belonging to the given logical cluster, resuming at continueToken if set.
//
// If endKey is non-empty, the scan stops before reaching endKey (exclusive).
// If limit is zero or negative, ScanPageSize is used.
//
// Values are decrypted using transformers (with the full storage key as
// authenticated context) before hashing so digests match across shards that
// use different storage prefixes or encryption keys.
func ScanEtcdDigests(
	ctx context.Context,
	kv clientv3.KV,
	leases LeaseTimeToLive,
	storagePrefix string,
	target logicalcluster.Name,
	continueToken, endKey string,
	limit int64,
	transformers value.ResourceTransformers,
) ([]migrationv1alpha1.EtcdEntryDigest, string, error) {
	entries, nextContinue, err := ScanEtcdEntries(ctx, kv, leases, storagePrefix, target, continueToken, endKey, limit, math.MaxInt64, transformers)
	if err != nil {
		return nil, "", err
	}
	digests := make([]migrationv1alpha1.EtcdEntryDigest, len(entries))
	for i, entry := range entries {
		sum := sha256.Sum256(entry.Value)
		digests[i] = migrationv1alpha1.EtcdEntryDigest{
			Key:        entry.Key,
			Hash:       hex.EncodeToString(sum[:]),
			TTLSeconds: entry.TTLSeconds,
		}
	}
	return digests, nextContinue, nil
}

// NextClusterRange seeks over unrelated cluster prefixes without reading their
// values. A trailing slash keeps similarly named clusters outside each range.
// Discovery still uses a shard-wide range, so etcd's range-counting cost remains.
func NextClusterRange(ctx context.Context, kv clientv3.KV, prefix, key string, target logicalcluster.Name) (string, string, error) {
	end := clientv3.GetPrefixRangeEnd(prefix)
	for key < end {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		resp, err := kv.Get(ctx, key, clientv3.WithRange(end), clientv3.WithKeysOnly(), clientv3.WithLimit(1))
		if err != nil {
			return "", "", fmt.Errorf("failed to discover cluster storage range: %w", err)
		}
		if len(resp.Kvs) == 0 {
			return "", "", nil
		}
		found := string(resp.Kvs[0].Key)
		// SplitKey expects a complete object key (at least four segments).
		if len(strings.Split(strings.TrimPrefix(found, prefix), "/")) < 4 {
			key = found + "\x00"
			continue
		}
		parts, ok := SplitKey(prefix, found, target)
		if !ok || parts.Rest == "" {
			key = found + "\x00"
			continue
		}
		clusterPrefix := parts.ClusterPrefix(prefix) + "/"
		if parts.Cluster == target {
			return clusterPrefix, found, nil
		}
		key = clientv3.GetPrefixRangeEnd(clusterPrefix)
	}
	return "", "", nil
}
