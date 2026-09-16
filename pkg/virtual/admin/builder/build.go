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
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/admin"
	adminauthorizer "github.com/kcp-dev/kcp/pkg/virtual/admin/authorizer"
)

// apiDomainKey is the single, static API domain of the shards view: the VW
// serves one fixed API set regardless of the caller.
const apiDomainKey = dynamiccontext.APIDomainKey(admin.VirtualWorkspaceName)

// BuildVirtualWorkspace builds the Admin workspace virtual workspace. Its
// first resource is a read-only, cache-backed aggregate view of every
// shard's Shard object.
//
// URL patterns:
//
//	/services/admin/apis/core.kcp.io/v1alpha1/shards            (plain clients, e.g. kubectl via `ws use :admin`)
//	/services/admin/clusters/<anything>/apis/core.kcp.io/...    (cluster-aware clients; the segment is accepted and ignored)
func BuildVirtualWorkspace(
	rootPathPrefix string,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	readyCh := make(chan struct{})

	vw := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}
			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("admin virtual workspace is not started")
			}
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			provider := &singleResourceAPIDefinitionSetProvider{
				config:                    mainConfig,
				cacheDynamicClusterClient: cacheDynamicClusterClient,
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
		{Name: admin.VirtualWorkspaceName, VirtualWorkspace: vw},
	}, nil
}

// digestURL accepts /services/admin and everything below it. An optional
// /clusters/<name-or-*> segment (added by cluster-aware clients) is parsed
// and stripped; the shards view is identical for every value.
func digestURL(urlPath, rootPathPrefix string) (cluster genericapirequest.Cluster, logicalPath string, accepted bool) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", false
	}

	prefix := strings.TrimSuffix(rootPathPrefix, "/")
	realPath := strings.TrimPrefix(urlPath, prefix)
	if realPath != "" && !strings.HasPrefix(realPath, "/") {
		return genericapirequest.Cluster{}, "", false
	}

	// the aggregate view is cluster-less; present it under the root cluster
	// name so cluster-scoped machinery has a stable, valid cluster.
	cluster = genericapirequest.Cluster{Name: core.RootCluster}

	if withoutClustersPrefix, ok := strings.CutPrefix(realPath, "/clusters/"); ok {
		parts := strings.SplitN(withoutClustersPrefix, "/", 2)
		path := logicalcluster.NewPath(parts[0])
		if path == logicalcluster.Wildcard {
			cluster = genericapirequest.Cluster{Wildcard: true}
		} else if name, ok := path.Name(); ok {
			cluster = genericapirequest.Cluster{Name: name}
		} else {
			return genericapirequest.Cluster{}, "", false
		}
		realPath = "/"
		if len(parts) > 1 {
			realPath += parts[1]
		}
		return cluster, strings.TrimSuffix(urlPath, realPath), true
	}

	return cluster, prefix, true
}

// singleResourceAPIDefinitionSetProvider serves the one fixed API of this
// virtual workspace, lazily constructing the serving info once.
type singleResourceAPIDefinitionSetProvider struct {
	config                    genericapiserver.CompletedConfig
	cacheDynamicClusterClient kcpdynamic.ClusterInterface

	lock   sync.Mutex
	cached apidefinition.APIDefinitionSet
}

func (p *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	if key != apiDomainKey {
		return nil, false, nil
	}

	p.lock.Lock()
	defer p.lock.Unlock()
	if p.cached != nil {
		return p.cached, true, nil
	}

	apiDef, err := provideShardsRestStorage(p.config, p.cacheDynamicClusterClient)
	if err != nil {
		return nil, false, err
	}

	p.cached = apidefinition.APIDefinitionSet{
		schema.GroupVersionResource{
			Group:    corev1alpha1.SchemeGroupVersion.Group,
			Version:  corev1alpha1.SchemeGroupVersion.Version,
			Resource: "shards",
		}: apiDef,
	}
	return p.cached, true, nil
}

var _ apidefinition.APIDefinitionSetGetter = &singleResourceAPIDefinitionSetProvider{}

func newAuthorizer() *authorization.Decorator {
	auth := adminauthorizer.NewAdminAuthorizer()
	return authorization.NewDecorator("virtual.admin.authorization.kcp.io", auth).AddAuditLogging().AddAnonymization()
}
