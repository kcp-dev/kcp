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
	"errors"
	"path"
	"sync"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/apiidentity"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/admin"
	adminauthorizer "github.com/kcp-dev/kcp/pkg/virtual/admin/authorizer"
)

// apiDomainKey is the single, static API domain of the Admin workspace: the
// VW serves one fixed API set regardless of the caller.
const apiDomainKey = dynamiccontext.APIDomainKey(admin.VirtualWorkspaceName)

// pathPatterns returns the mux patterns of the virtual workspace:
// rootPathPrefix itself and, for cluster-aware clients, below an optional
// clusters/<cluster> segment. The views are identical for every value.
func pathPatterns(rootPathPrefix string) []string {
	return []string{
		path.Clean(rootPathPrefix),
		path.Join(rootPathPrefix, "clusters/{cluster}"),
	}
}

// BuildVirtualWorkspace builds the Admin workspace virtual workspace. It
// serves two cache-backed, installation-wide views: every shard's Shard
// object, writable for the allow-listed operational fields, and the
// read-only APIIdentity registry.
//
// URL patterns:
//
//	/services/admin/apis/core.kcp.io/v1alpha1/shards            (plain clients, e.g. kubectl via `ws use :admin`)
//	/services/admin/apis/admin.kcp.io/v1alpha1/apiidentities
//	/services/admin/clusters/<anything>/apis/...                (cluster-aware clients; the segment is accepted and ignored)
//
// identitiesSynced reports whether the identity view has seen every
// APIExport the cache server holds; the workspace is not ready before.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
	identities *apiidentity.Projector,
	identitiesSynced func() bool,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	readyCh := make(chan struct{})

	vw := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveRootPath),
		Authorizer:       newAuthorizer(),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
			default:
				return errors.New("admin virtual workspace is not started")
			}
			if !identitiesSynced() {
				return errors.New("the identity view has not synced yet")
			}
			return nil
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			provider := &adminAPIDefinitionSetProvider{
				config:                    mainConfig,
				cacheDynamicClusterClient: cacheDynamicClusterClient,
				identities:                identities,
			}
			if err := mainConfig.AddPostStartHook(admin.VirtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
				close(readyCh)
				return nil
			}); err != nil {
				return nil, err
			}
			return provider, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: admin.VirtualWorkspaceName, Patterns: pathPatterns(rootPathPrefix), VirtualWorkspace: vw},
	}, nil
}

// resolveRootPath presents the cluster-less aggregate view under the root
// cluster name, so cluster-scoped machinery has a stable, valid cluster,
// unless the request names a cluster.
func resolveRootPath(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	if match.Value(framework.ClusterWildcard) == "" {
		ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: core.RootCluster})
	}
	return true, match.PrefixToStrip(), dynamiccontext.WithAPIDomainKey(ctx, apiDomainKey)
}

// adminAPIDefinitionSetProvider serves the fixed API set of this virtual
// workspace, lazily constructing the serving info once.
type adminAPIDefinitionSetProvider struct {
	config                    genericapiserver.CompletedConfig
	cacheDynamicClusterClient kcpdynamic.ClusterInterface
	identities                *apiidentity.Projector

	lock   sync.Mutex
	cached apidefinition.APIDefinitionSet
}

func (p *adminAPIDefinitionSetProvider) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	if key != apiDomainKey {
		return nil, false, nil
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	if p.cached != nil {
		return p.cached, true, nil
	}

	shardsDef, err := provideShardsRestStorage(p.config, p.cacheDynamicClusterClient)
	if err != nil {
		return nil, false, err
	}
	apiIdentitiesDef, err := provideAPIIdentitiesRestStorage(p.config, p.identities)
	if err != nil {
		shardsDef.TearDown()
		return nil, false, err
	}

	p.cached = apidefinition.APIDefinitionSet{
		corev1alpha1.SchemeGroupVersion.WithResource("shards"):         shardsDef,
		adminv1alpha1.SchemeGroupVersion.WithResource("apiidentities"): apiIdentitiesDef,
	}
	return p.cached, true, nil
}

var _ apidefinition.APIDefinitionSetGetter = &adminAPIDefinitionSetProvider{}

func newAuthorizer() *authorization.Decorator {
	auth := adminauthorizer.NewAdminAuthorizer()
	return authorization.NewDecorator("virtual.admin.authorization.kcp.io", auth).AddAuditLogging().AddAnonymization()
}
