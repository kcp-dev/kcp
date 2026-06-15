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

package logicalclustermigration

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
)

// deleteOriginData removes all etcd data belonging to the given logical cluster.
func (c *Controller) deleteOriginData(ctx context.Context, lcName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("cleaning up origin data via etcd", "logicalCluster", lcName)

	prefix := c.etcdStoragePrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	// TODO(ntnn): This isn't very solid and just assumes the happy
	// path. The deletion should probably stop and requeue on any error.

	prefixes := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		defer close(errCh)
		errCh <- c.scanEtcdKeys(ctx, prefix, lcName, prefixes)
	}()

	var deleteErrors []error
	for p := range prefixes {
		logger.V(4).Info("deleting etcd prefix", "prefix", p)
		if _, err := c.etcdClient.Delete(ctx, p, clientv3.WithPrefix()); err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("failed to delete prefix %s: %w", p, err))
		}
	}

	if err := <-errCh; err != nil {
		deleteErrors = append(deleteErrors, err)
	}

	if len(deleteErrors) > 0 {
		return fmt.Errorf("origin cleanup encountered %d errors: %v", len(deleteErrors), deleteErrors)
	}

	return nil
}

// scanEtcdKeys scans etcd and emits per-cluster deletion prefixes for all keys belonging to targetLogicalCluster.
func (c *Controller) scanEtcdKeys(ctx context.Context, prefix string, targetLogicalCluster logicalcluster.Name, out chan<- string) error {
	defer close(out)

	key := prefix
	for {
		// While this logic is pulling batches of keys it only processes
		// a batch until it found a matching key; then the prefix for
		// this "tree" is written to the channel for the consumer to
		// delete the key.
		// That means in small trees we might retrieve the same keys
		// a few times.
		// TODO(ntnn): Arguably it would be better to process the
		// full batch but this is simpler for now.
		resp, err := c.etcdClient.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithKeysOnly(),
			clientv3.WithLimit(kcpetcd.ScanPageSize),
		)
		if err != nil {
			return fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, kv := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return err
			}

			k := string(kv.Key)
			split, ok := kcpetcd.SplitKey(prefix, k, targetLogicalCluster)
			if !ok {
				// not a relevant etcd key
				continue
			}

			builtPrefix := split.ClusterPrefix(prefix)

			if split.Cluster == targetLogicalCluster {
				// key belongs to this cluster, pass the prefix into the channel
				out <- builtPrefix
			}

			// skip to the next subtree
			key = split.ClusterPrefix(builtPrefix) + "\x00"
			break
		}

		if !resp.More {
			return nil
		}

		if !strings.HasSuffix(key, "\x00") {
			// Didn't instruct to skip any keys, continue from last key
			// in the batch
			key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
		}

		continue
	}
}
