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
	// exemptPathPrefixes allows some paths to pass without adding a context.
	exemptPathPrefixes := []string{
		// Kube clients expect the /openapi endpoint to be available to
		// e.g. retrieve schemas. E.g. kubectl-edit fetches openapi
		// specs to validate the edited resource.
		"/openapi",
		// logical cluster objects must still be editable to lifecylce the lc.
		"/apis/core.kcp.io/v1alpha1/logicalclusters",
		// Skip adding contexts to requests with LogicalClusterDump,
		// contexts for migrating LCs are cancelled and stay cancelled
		// until they are migrated.
		migrationDumpHandlerPath,
	}

	return func(w http.ResponseWriter, req *http.Request) {
		for _, prefix := range exemptPathPrefixes {
			if strings.HasPrefix(req.URL.Path, prefix) {
				handler.ServeHTTP(w, req)
				return
			}
		}

		cluster := request.ClusterFrom(req.Context())

		var clusterPath logicalcluster.Path
		// Handling the differing cases in a switch to prevent crossing the logic.
		switch {
		case cluster == nil:
			handler.ServeHTTP(w, req)
			return
		case cluster.Wildcard:
			// Explicitly including wildcard requests. When a cluster is
			// cancelled the wildcard connections must also be cancelled in
			// case a wildcard watch targets objects in the affected logical
			// cluster.
			clusterPath = logicalcluster.Wildcard
		case cluster.Name.Empty():
			// .Name.Empty must be checked after .Wildcard as .Name will
			// be empty if .Wildcard is true.
			handler.ServeHTTP(w, req)
			return
		case strings.HasPrefix(cluster.Name.String(), "system:"):
			// The per-shard system workspaces do not need a context as
			// they can't be migrated and shouldn't be put in inactive
			// state.
			handler.ServeHTTP(w, req)
			return
		default:
			clusterPath = cluster.Name.Path()
		}

		ctx, cleanup := mgr.Context(req.Context(), clusterPath)
		defer cleanup()
		handler.ServeHTTP(w, req.WithContext(ctx))
	}
}
