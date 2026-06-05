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
)

// etcdScanPageSize is the number of keys fetched per etcd Range request when
// scanning the storage for the keys of a logical cluster.
const etcdScanPageSize int64 = 1000

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

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

	logger.V(2).Info("dumping logical cluster from etcd", "cluster", cluster.Name)

	entries, err := scanEtcdEntries(ctx, h.etcdClient, h.etcdStoragePrefix, cluster.Name)
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
			Entries: entries,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error(err, "failed to write dump response")
	}
}

// scanEtcdEntries returns every etcd key/value pair belonging to the given
// logical cluster.
//
// TODO: stream entries as NDJSON so the response doesn't have to be buffered
// in memory.
//
// TODO: paginate via spec.Continue / status.Continue so a single dump call
// doesn't have to hold all keys in memory.
func scanEtcdEntries(ctx context.Context, etcdClient *clientv3.Client, storagePrefix string, target logicalcluster.Name) ([]migrationv1alpha1.EtcdEntry, error) {
	prefix := storagePrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	var entries []migrationv1alpha1.EtcdEntry
	key := prefix
	for {
		resp, err := etcdClient.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithLimit(etcdScanPageSize),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, kv := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// TODO(ntnn): This isn't super efficient. For large LCs
			// this will re-parse thousands of keys. It would be better
			// to cache the known-good prefixes and to continue from
			// there, however considering that this should move towards
			// pagination or streaming it seems like premature
			// optimization. Just something to keep in mind when working
			// on this.
			if !belongsToLogicalCluster(prefix, string(kv.Key), target) {
				continue
			}
			entries = append(entries, migrationv1alpha1.EtcdEntry{
				Key:   strings.TrimPrefix(string(kv.Key), prefix),
				Value: append([]byte(nil), kv.Value...),
			})
		}

		if !resp.More {
			return entries, nil
		}
		key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

// belongsToLogicalCluster reports whether an etcd key belongs to the given logical cluster.
func belongsToLogicalCluster(prefix, key string, target logicalcluster.Name) bool {
	rest := strings.TrimPrefix(key, prefix)
	if rest == key {
		return false
	}
	parts := strings.SplitN(rest, "/", 5)
	if len(parts) < 3 {
		return false
	}
	//	<prefix>/<group>/<resource>/customresources/<lc>/...
	if parts[2] == "customresources" {
		if len(parts) < 4 {
			return false
		}
		return logicalcluster.Name(parts[3]) == target
	}
	//	<prefix>/<group>/<resource>/<lc>/...
	return logicalcluster.Name(parts[2]) == target
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, w, r)
}
