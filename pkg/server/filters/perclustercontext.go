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
	"strings"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/contextmanager"
)

// WithPerClusterContext injects a multiple-parent context for each request
// that is bound by the requests' context and the cluster-specific context.
//
// This handler must run only after the client-provided information has
// been normalized to a logical cluster id.
//
// This is used e.g. to cancel active connections when a logical cluster
// is being migrated.
func WithPerClusterContext(handler http.Handler, mgr *contextmanager.Manager[logicalcluster.Path]) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		cluster := request.ClusterFrom(req.Context())
		// Explicitly including wildcard requests. When a cluster is
		// cancelled the wildcard connections must also be cancelled in
		// case a wildcard watch targets objects in the affected logical
		// cluster.
		if cluster == nil || cluster.Name.Empty() || strings.HasPrefix(cluster.Name.String(), "system:") {
			handler.ServeHTTP(w, req)
			return
		}

		var clusterPath logicalcluster.Path
		switch {
		case cluster.Wildcard:
			clusterPath = logicalcluster.Wildcard
		default:
			clusterPath = cluster.Name.Path()
		}

		ctx, cleanup := mgr.ContextFor(req.Context(), clusterPath)
		defer cleanup()
		handler.ServeHTTP(w, req.WithContext(ctx))
	}
}
