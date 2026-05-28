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

	"k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/authorization/shardpaths"
)

// WithShardLevelPaths enforces that shard-level URLs (see
// pkg/authorization/shardpaths) are only served at the top of the shard's HTTP
// path tree.
//
// It must run AFTER WithLocalProxy so the cluster context reflects whether the
// request was made through a /clusters/<ws>/ prefix.
//
//   - workspace-scoped requests (cluster context already set to a non-root
//     logical cluster) are rejected with 501 Not Implemented: the data exposed
//     at these paths is shard-wide and has no per-workspace meaning.
//   - top-level requests (no cluster context) are rewritten to evaluate
//     authorization against the root workspace. A kcp-admin can therefore grant
//     scrape access via a ClusterRoleBinding in :root referring to the
//     system:kcp:metrics-reader role (bootstrapped) without exposing any other
//     root-workspace privileges.
func WithShardLevelPaths(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !shardpaths.Paths.Has(req.URL.Path) {
			handler.ServeHTTP(w, req)
			return
		}

		cluster := request.ClusterFrom(req.Context())
		if cluster != nil && !cluster.Name.Empty() && cluster.Name != core.RootCluster {
			audit.AddAuditAnnotation(req.Context(), "shardpaths.kcp.io/rejected", req.URL.Path)
			http.Error(w, "shard-level endpoint not available at workspace scope", http.StatusNotImplemented)
			return
		}

		ctx := request.WithCluster(req.Context(), request.Cluster{Name: core.RootCluster})
		handler.ServeHTTP(w, req.WithContext(ctx))
	}
}
