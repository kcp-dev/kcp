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

package filters

import (
	"net/http"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// WithBlockMigratingLogicalClusters rejects requests to logical clusters that are currently being migrated.
//
// This is very similar to WithBlockInactiveLogicalClusters, however the
// migration requires that no client except other shards can access the
// logical cluster, otherwise operators running with admin rights might
// modify objects after they were migrated, producing an inconsistent
// state.
//
// Most requests are rejected with 503 Retry-After so clients transparently
// retry once the migration completes. List and watch requests that carry a
// resourceVersion are a special case: a migration moves the logical cluster
// to a different shard with an independent resource version space, so the
// resource version the client is resuming from is foreign to the destination
// shard. Replying Retry-After to those would make the destination shard's
// watch cache block on resourceVersionMatch=NotOlderThan until its own,
// unrelated resource version counter happens to catch up - which may take
// arbitrarily long (see https://github.com/kcp-dev/kcp/issues/4250). Instead
// those requests are answered with 410 Gone (Expired) and no Retry-After
// header, so the REST client surfaces the error instead of swallowing it and
// the reflector drops its cached resource version and relists from scratch,
// which resolves cleanly against the destination shard.
func WithBlockMigratingLogicalClusters(handler http.Handler, isMigrating func(logicalcluster.Name) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cluster := request.ClusterFrom(req.Context())
		if cluster == nil {
			handler.ServeHTTP(w, req)
			return
		}

		if !isMigrating(cluster.Name) {
			handler.ServeHTTP(w, req)
			return
		}

		userInfo, ok := request.UserFrom(req.Context())
		if ok && slices.Contains(userInfo.GetGroups(), bootstrappolicy.SystemExternalLogicalClusterAdmin) {
			// allow system:kcp:external-logical-cluster-admin to pass,
			// required for the destination shard to get
			// logicalclusterdump from the migrating workspace
			handler.ServeHTTP(w, req)
			return
		}

		logger := klog.FromContext(req.Context())

		// Tell list/watch clients that carry a resourceVersion to relist
		// from scratch rather than retrying against the now-foreign
		// resource version space of the destination shard.
		if requestInfo, ok := request.RequestInfoFrom(req.Context()); ok && requestInfo.IsResourceRequest &&
			(requestInfo.Verb == "list" || requestInfo.Verb == "watch") {
			if rv := req.URL.Query().Get("resourceVersion"); rv != "" && rv != "0" {
				logger.V(2).Info("migrating filter: expiring resource version for migrating cluster", "cluster", cluster.Name, "resourceVersion", rv)
				responsewriters.ErrorNegotiated(
					apierrors.NewResourceExpired("logical cluster is being migrated to another shard; relist required"),
					errorCodecs, schema.GroupVersion{}, w, req,
				)
				return
			}
		}

		logger.V(2).Info("migrating filter: blocking request to migrating cluster", "cluster", cluster.Name)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "logical cluster is being migrated", http.StatusServiceUnavailable)
	}
}
