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

package builder

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	transport "k8s.io/client-go/transport"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/handler"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
)

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	// Build clients that talk directly to the local shard.
	localCfg := rest.CopyConfig(cfg)

	vw := &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			if !ok {
				return false, "", requestContext
			}

			if cluster.Wildcard {
				return false, "", requestContext
			}

			completedContext = genericapirequest.WithCluster(requestContext, cluster)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
			if a.GetUser() == nil {
				return authorizer.DecisionDeny, "no user info", nil
			}
			if !slices.Contains(a.GetUser().GetGroups(), bootstrappolicy.SystemExternalLogicalClusterAdmin) {
				return authorizer.DecisionDeny, "user is not in group " + bootstrappolicy.SystemExternalLogicalClusterAdmin, nil
			}
			return authorizer.DecisionAllow, "", nil
		}),
		ReadyChecker: framework.ReadyFunc(func() error {
			return nil
		}),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			return newMigratingHandler(localCfg)
		}),
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: migratingworkspaces.VirtualWorkspaceName, VirtualWorkspace: vw},
	}, nil
}

// migratingHandler is a narrow reverse proxy that forwards the
// LogicalClusterDump endpoint to the local shard. Anything else under the
// VW prefix is rejected.
type migratingHandler struct {
	proxy *httputil.ReverseProxy
	host  string
}

func newMigratingHandler(cfg *rest.Config) (http.Handler, error) {
	target, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, err
	}
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Director:  func(*http.Request) {}, // request is fully rewritten before ServeHTTP forwards
	}
	return &migratingHandler{proxy: proxy, host: target.Host}, nil
}

func (h *migratingHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	logger := klog.FromContext(ctx)

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		http.Error(w, "no cluster in context", http.StatusBadRequest)
		return
	}

	if req.URL.Path != migratingworkspaces.HandlerPath {
		http.NotFound(w, req)
		return
	}

	outReq := req.Clone(ctx)
	outReq.URL.Scheme = "https"
	outReq.URL.Host = h.host
	outReq.URL.Path = cluster.Name.Path().RequestPath() + migratingworkspaces.HandlerPath
	outReq.Host = h.host
	outReq.RequestURI = ""

	// Forward the caller's identity via impersonation so the dump handler
	// sees the real user (e.g. system:kcp:external-logical-cluster-admin).
	if userInfo, ok := genericapirequest.UserFrom(ctx); ok {
		outReq.Header.Set(transport.ImpersonateUserHeader, userInfo.GetName())
		for _, group := range userInfo.GetGroups() {
			outReq.Header.Add(transport.ImpersonateGroupHeader, group)
		}
	}

	logger.V(2).Info("migrating VW proxying dump", "cluster", cluster.Name, "url", outReq.URL.String())
	h.proxy.ServeHTTP(w, outReq)
}

func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", false
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests look like:
	//   /services/migratingworkspaces/clusters/<lc-name>/apis/...
	//                                └─── withoutRootPathPrefix
	if !strings.HasPrefix(withoutRootPathPrefix, "clusters/") {
		return genericapirequest.Cluster{}, "", false
	}

	withoutClustersPrefix := strings.TrimPrefix(withoutRootPathPrefix, "clusters/")
	parts := strings.SplitN(withoutClustersPrefix, "/", 2)
	clusterPath := logicalcluster.NewPath(parts[0])

	realPath := "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	cluster = genericapirequest.Cluster{}
	if clusterPath == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		var ok bool
		cluster.Name, ok = clusterPath.Name()
		if !ok {
			return genericapirequest.Cluster{}, "", false
		}
	}

	// Strip the entire prefix including the cluster path, leaving just the
	// kube API path (e.g. /api/v1/configmaps).
	return cluster, strings.TrimSuffix(urlPath, realPath), true
}
