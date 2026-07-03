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

package migrationdump

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

// defaultDumpMaxBytes is the default cap on the total size of entry values
// returned in a single LogicalClusterDump page, used when the request
// doesn't specify spec.maxBytes.
const defaultDumpMaxBytes int64 = 2 * 1024 * 1024

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned, &metav1.Status{})
}

// migratingClusters is a local interface for checking if a cluster is migrating.
type migratingClusters interface {
	IsMigrating(name logicalcluster.Name) bool
}

// Handler serves POST requests to HandlerPath by dumping every etcd entry
// that belongs to the cluster carried in the request context.
type Handler struct {
	etcdClient               *clientv3.Client
	etcdStoragePrefix        string
	migratingLogicalClusters migratingClusters
}

func NewHandler(
	etcdClient *clientv3.Client,
	etcdStoragePrefix string,
	migratingLogicalClusters migratingClusters,
) *Handler {
	return &Handler{
		etcdClient:               etcdClient,
		etcdStoragePrefix:        etcdStoragePrefix,
		migratingLogicalClusters: migratingLogicalClusters,
	}
}

func (h *Handler) Close() error {
	return h.etcdClient.Close()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := klog.FromContext(ctx)

	if r.Method != http.MethodPost {
		writeError(w, r, apierrors.NewMethodNotSupported(
			migrationv1alpha1.Resource("logicalclusterdumps"),
			r.Method,
		))
		return
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		writeError(w, r, apierrors.NewBadRequest("no cluster in context"))
		return
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok || user == nil {
		writeError(w, r, apierrors.NewUnauthorized("no user info"))
		return
	}
	if !slices.Contains(user.GetGroups(), bootstrappolicy.SystemExternalLogicalClusterAdmin) {
		writeError(w, r, apierrors.NewForbidden(
			migrationv1alpha1.Resource("logicalclusterdumps"),
			"",
			fmt.Errorf("user is not in group %s", bootstrappolicy.SystemExternalLogicalClusterAdmin),
		))
		return
	}

	if !h.migratingLogicalClusters.IsMigrating(cluster.Name) {
		writeError(w, r, apierrors.NewNotFound(
			migrationv1alpha1.Resource("logicalclusterdumps"),
			string(cluster.Name),
		))
		return
	}

	// Decode the request body. Spec is empty today so we don't actually
	// need anything from it, but parsing it ensures the client sent
	// something resembling the expected type.
	var dump migrationv1alpha1.LogicalClusterDump
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&dump); err != nil {
			writeError(w, r, apierrors.NewBadRequest(fmt.Sprintf("failed to decode request body: %v", err)))
			return
		}
	}

	logger.V(2).Info("dumping logical cluster page from etcd", "cluster", cluster.Name, "continue", dump.Spec.Continue)

	entries, nextContinue, err := scanEtcdEntries(ctx, h.etcdClient, h.etcdStoragePrefix, cluster.Name, dump.Spec.Continue, dump.Spec.Limit, dump.Spec.MaxBytes)
	if err != nil {
		writeError(w, r, apierrors.NewInternalError(fmt.Errorf("failed to dump logical cluster: %w", err)))
		return
	}

	resp := &migrationv1alpha1.LogicalClusterDump{
		TypeMeta: metav1.TypeMeta{
			APIVersion: migrationv1alpha1.SchemeGroupVersion.String(),
			Kind:       "LogicalClusterDump",
		},
		ObjectMeta: dump.ObjectMeta,
		Status: migrationv1alpha1.LogicalClusterDumpStatus{
			Entries:  entries,
			Continue: nextContinue,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error(err, "failed to write dump response")
	}
}

// scanEtcdEntries returns a single page of etcd key/value pairs belonging
// to the given logical cluster, resuming right after continueToken if set.
//
// The page stops growing once it holds limit entries or once adding another
// entry would push the accumulated value size past maxBytes, whichever
// happens first (a single entry larger than maxBytes is still returned
// alone, so the scan always makes progress). If limit or maxBytes is zero
// or negative, a default is used.
//
// The second return value is the continue token for the next page, or the
// empty string if the scan reached the end of the logical cluster's
// keyspace.
func scanEtcdEntries(ctx context.Context, kv clientv3.KV, storagePrefix string, target logicalcluster.Name, continueToken string, limit, maxBytes int64) ([]migrationv1alpha1.EtcdEntry, string, error) {
	prefix := storagePrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if limit <= 0 {
		limit = kcpetcd.ScanPageSize
	}
	if maxBytes <= 0 {
		maxBytes = defaultDumpMaxBytes
	}

	key := prefix
	if continueToken != "" {
		key = prefix + strings.TrimPrefix(continueToken, "/")
	}

	var entries []migrationv1alpha1.EtcdEntry
	var totalBytes int64
	for {
		resp, err := kv.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithLimit(kcpetcd.ScanPageSize),
		)
		if err != nil {
			return nil, "", fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, entry := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			if !kcpetcd.BelongsToCluster(prefix, string(entry.Key), target) {
				continue
			}

			if int64(len(entries)) >= limit || (len(entries) > 0 && totalBytes+int64(len(entry.Value)) > maxBytes) {
				return entries, strings.TrimPrefix(string(entry.Key), prefix), nil
			}

			entries = append(entries, migrationv1alpha1.EtcdEntry{
				Key:   strings.TrimPrefix(string(entry.Key), prefix),
				Value: append([]byte(nil), entry.Value...),
			})
			totalBytes += int64(len(entry.Value))
		}

		if !resp.More {
			return entries, "", nil
		}
		key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, w, r)
}
