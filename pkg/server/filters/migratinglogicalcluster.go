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

		klog.FromContext(req.Context()).V(2).Info("migrating filter: blocking request to migrating cluster", "cluster", cluster.Name)
		w.Header().Set("Retry-After", "1")
		http.Error(w, "logical cluster is being migrated", http.StatusServiceUnavailable)
	}
}
