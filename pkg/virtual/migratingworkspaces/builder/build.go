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
	"path"
	"slices"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	transport "k8s.io/client-go/transport"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/handler"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
)

// pathPattern returns the mux pattern of the virtual workspace below rootPathPrefix.
func pathPattern(rootPathPrefix string) string {
	return path.Join(rootPathPrefix, "clusters/{cluster}")
}

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	// Build clients that talk directly to the local shard.
	localCfg := rest.CopyConfig(cfg)

	vw := &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveRootPath),
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
		{
			Name:             migratingworkspaces.VirtualWorkspaceName,
			Patterns:         []string{pathPattern(rootPathPrefix)},
			VirtualWorkspace: vw,
		},
	}, nil
}

// resolveRootPath accepts requests for a specific cluster only.
func resolveRootPath(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	if cluster := genericapirequest.ClusterFrom(ctx); cluster == nil || cluster.Wildcard {
		return false, "", ctx
	}
	return true, match.PrefixToStrip(), ctx
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
		ErrorLog:  logging.NewReverseProxyErrorLog(klog.Background()),
		Rewrite:   func(*httputil.ProxyRequest) {}, // request is fully rewritten before ServeHTTP forwards
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
