/*
Copyright 2025 The kcp Authors.

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
	"fmt"
	"strings"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/replication"
	replicationauthorizer "github.com/kcp-dev/kcp/pkg/virtual/replication/authorizer"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/controllers/apireconciler"
)

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	cachedResourceContent := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(kubeClusterClient, localKcpInformers, globalKcpInformers),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("replication virtual workspace controllers are not started")
			}
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			// Define informers that need to be waited for in the post-start hook. Calling Informer() starts the informer,
			// and we need to do that before the SharedInformerFactory.Start() is called in cmd/virtual-workspaces/cmd.go.

			globalInformers := map[string]cache.SharedIndexInformer{
				"cachedresources":              globalKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"cachedresourceendpointslices": globalKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Informer(),
				"apiexports":                   globalKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apiresourceschemas":           globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			localInformers := map[string]cache.SharedIndexInformer{
				"cachedresources":              localKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"cachedresourceendpointslices": localKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Informer(),
				"apiexports":                   localKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apibindings":                  localKcpInformers.Apis().V1alpha2().APIBindings().Informer(),
				"apiresourceschemas":           localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			// Install indexers.

			// CachedResource & friends indexers.
			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)
			indexers.AddIfNotPresentOrDie(
				localKcpInformers.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)

			// APIExport indexers.
			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)
			indexers.AddIfNotPresentOrDie(
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)

			// APIBinding indexers.
			indexers.AddIfNotPresentOrDie(localKcpInformers.Apis().V1alpha2().APIBindings().Informer().GetIndexer(), cache.Indexers{
				indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
			})

			apiReconciler, err := apireconciler.NewAPIReconciler(
				localKcpInformers,
				globalKcpInformers,
				func(apiResourceSchema *apisv1alpha1.APIResourceSchema, cachedResource *cachev1alpha1.CachedResource, export *apisv1alpha2.APIExport) (apidefinition.APIDefinition, error) {
					return provideReadOnlyRestStorage(
						mainConfig,
						cacheDynamicClusterClient,
						apiResourceSchema,
						cachedResource,
						export,
					)
				},
			)
			if err != nil {
				return nil, err
			}

			// Wait for caches to be synced and start the APIReconciler controller.

			if err := mainConfig.AddPostStartHook(replication.VirtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range globalInformers {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "global informer not synced")
						return nil
					}
				}

				for name, informer := range localInformers {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "local informer not synced")
						return nil
					}
				}

				go apiReconciler.Start(hookContext)

				return nil
			}); err != nil {
				return nil, err
			}

			return apiReconciler, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: replication.VirtualWorkspaceName, VirtualWorkspace: cachedResourceContent},
	}, nil
}

func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/apiexport/root:org:ws/<CachedResourceEndpointSlice-name>/clusters/*/api/v1/configmaps
	//                     └────────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	endpointSliceClusterName, endpointSliceName := parts[0], parts[1]
	if endpointSliceClusterName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}
	if endpointSliceName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 2 {
		realPath += parts[2]
	}

	//  /services/apiexport/root:org:ws/<CachedResourceEndpointSlice-name>/clusters/*/api/v1/configmaps
	//                     ┌──────────────────────────────────────────────┘
	// We are now here: ───┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	path := logicalcluster.NewPath(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	cluster = genericapirequest.Cluster{}
	if path == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		var ok bool
		cluster.Name, ok = path.Name()
		if !ok {
			return genericapirequest.Cluster{}, "", "", false
		}
	}

	key := fmt.Sprintf("%s/%s", endpointSliceClusterName, endpointSliceName)
	return cluster, dynamiccontext.APIDomainKey(key), strings.TrimSuffix(urlPath, realPath), true
}

func newAuthorizer(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) authorizer.Authorizer {
	contentAuthorizer := replicationauthorizer.NewContentAuthorizer(kubeClusterClient, localKcpInformers, globalKcpInformers)
	contentAuthorizer = authorization.NewDecorator("virtual.replication.content.authorization.kcp.io", contentAuthorizer).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	return contentAuthorizer
}
