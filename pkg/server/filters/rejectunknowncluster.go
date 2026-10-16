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
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/authorization/shardpaths"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

// unknownClusterGroups may address logical clusters the index does not know.
var unknownClusterGroups = sets.New(
	// Loopback client during bootstrap
	bootstrap.SystemMastersGroup,
	// Workspace scheduler creating the LogicalCluster of a new workspace
	bootstrap.SystemLogicalClusterAdmin,
	// Workspace controller finishing initialization of the new cluster
	bootstrap.SystemExternalLogicalClusterAdmin,
	// kcp components, like the virtual-workspace, send stateless
	// TokenReviews and SubjectAccessReviews for clusters hosted on
	// another shard.
	bootstrap.SystemKcpAdminGroup,
)

// WithRejectUnknownCluster rejects requests for a logical cluster the index did not resolve.
// An exception is made for requests of kcp-internal groups.
// Must run after authentication and after lookup.WithClusterResolution.
func WithRejectUnknownCluster(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		cluster := request.ClusterFrom(ctx)
		if cluster == nil || cluster.Name.Empty() {
			// not a /clusters/ request
			handler.ServeHTTP(w, req)
			return
		}

		if cluster.Wildcard {
			// a wildcard request, those are gated separately
			handler.ServeHTTP(w, req)
			return
		}

		if lookup.ShardNameFrom(ctx) != "" {
			// a /clusters/ request that has been resolved by the index
			handler.ServeHTTP(w, req)
			return
		}

		if info, ok := request.RequestInfoFrom(ctx); ok && !info.IsResourceRequest && shardpaths.Paths.Has(info.Path) {
			// a non-resource shard-level request, e.g. /metrics
			handler.ServeHTTP(w, req)
			return
		}

		user, ok := request.UserFrom(ctx)
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("no user in context")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}
		if unknownClusterGroups.HasAny(user.GetGroups()...) {
			handler.ServeHTTP(w, req)
			return
		}

		klog.FromContext(ctx).WithValues("cluster", cluster.Name, "user", user.GetName()).V(4).Info("rejecting request for unknown logical cluster")
		responsewriters.ErrorNegotiated(
			apierrors.NewForbidden(
				schema.GroupResource{Group: tenancyv1alpha1.SchemeGroupVersion.Group, Resource: "workspaces"},
				cluster.Name.String(),
				errors.New(authorization.WorkspaceAccessNotPermittedReason),
			),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
	})
}
