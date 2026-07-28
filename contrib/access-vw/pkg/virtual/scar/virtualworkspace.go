package scar

import (
	"context"
	"errors"
	"strings"

	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/fixedgvs"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	accessv1alpha1 "github.com/kcp-dev/kcp/contrib/access-vw/pkg/apis/access/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/virtual"
)

const VirtualWorkspaceName = "access"

// RootPath is the URL prefix the front-proxy routes to this VW.
// SCAR is served at RootPath + /apis/access.kcp.io/v1alpha1/selfclusteraccessreviews.
const RootPath = "/services/" + VirtualWorkspaceName

// NewVirtualWorkspace builds the Access VW: a fixed-group-version
// delegated apiserver serving the access.kcp.io/v1alpha1 API with the
// selfclusteraccessreviews REST storage, backed by the shared access
// graph.
func NewVirtualWorkspace(g *graph.Graph) rootapiserver.NamedVirtualWorkspace {
	vw := &fixedgvs.FixedGroupVersionsVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (bool, string, context.Context) {
			if urlPath != RootPath && !strings.HasPrefix(urlPath, RootPath+"/") {
				return false, "", ctx
			}
			return true, RootPath, ctx
		}),
		Authorizer: virtual.AuthenticatedOnlyAuthorizer(),
		ReadyChecker: framework.ReadyFunc(func() error {
			if !g.Ready() {
				return errors.New("access graph has not completed its initial sync")
			}
			return nil
		}),
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion: accessv1alpha1.SchemeGroupVersion,
				AddToScheme:  accessv1alpha1.AddToScheme,
				BootstrapRestResources: func(_ genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
					return map[string]fixedgvs.RestStorageBuilder{
						"selfclusteraccessreviews": func(_ genericapiserver.CompletedConfig) (restStorage.Storage, error) {
							return NewREST(g), nil
						},
					}, nil
				},
			},
		},
	}

	return rootapiserver.NamedVirtualWorkspace{
		Name:             VirtualWorkspaceName,
		VirtualWorkspace: &virtual.WithoutAdmission{CoreVirtualWorkspace: vw},
	}
}
