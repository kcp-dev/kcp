/*
Copyright 2025 The KCP Authors.

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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
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
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
	cachev1alpha1listers "github.com/kcp-dev/sdk/client/listers/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	cachedresourcesreplication "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/replication"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
	replicationauthorizer "github.com/kcp-dev/kcp/pkg/virtual/replication/authorizer"
)

func listAPIBindingsByCachedResource(identityHash string, gr schema.GroupResource, globalAPIExportIndexer cache.Indexer, apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer) ([]*apisv1alpha2.APIBinding, error) {
	exports, err := indexers.ByIndex[*apisv1alpha2.APIExport](globalAPIExportIndexer, indexers.APIExportByVirtualResourceIdentitiesAndGRs, indexers.VirtualResourceIdentityAndGRKey(identityHash, gr))
	if err != nil {
		return nil, err
	}

	var bindings []*apisv1alpha2.APIBinding
	for _, export := range exports {
		exportBindings, err := listAPIBindingsByAPIExport(apiBindingInformer, export)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, exportBindings...)
	}
	return bindings, err
}

func listClustersInBindings(bindings []*apisv1alpha2.APIBinding) sets.Set[logicalcluster.Name] {
	s := sets.New[logicalcluster.Name]()
	for _, binding := range bindings {
		s.Insert(logicalcluster.From(binding))
	}
	return s
}

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	cachedResourceContent := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			targetCluster, apiDomain, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			if !ok {
				return false, "", requestContext
			}
			_, err := apidomainkey.Parse(apiDomain)
			if err != nil {
				return false, "", requestContext
			}

			completedContext = genericapirequest.WithCluster(requestContext, targetCluster)
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
				"cachedobjects":      globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer(),
				"cachedresources":    globalKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"apiexports":         globalKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apiresourceschemas": globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			localInformers := map[string]cache.SharedIndexInformer{
				"cachedresources":    localKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"apiexports":         localKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apibindings":        localKcpInformers.Apis().V1alpha2().APIBindings().Informer(),
				"apiresourceschemas": localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			// Install indexers.

			// CachedResources indexers.
			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer().GetIndexer(),
				cache.Indexers{
					cachedresourcesreplication.ByGVRAndLogicalClusterAndNamespace: cachedresourcesreplication.IndexByGVRAndLogicalClusterAndNamespace,
				},
			)

			// APIExport indexers.
			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.APIExportByIdentity:                        indexers.IndexAPIExportByIdentity,
					indexers.ByLogicalClusterPathAndName:                indexers.IndexByLogicalClusterPathAndName,
					indexers.APIExportByVirtualResourceIdentitiesAndGRs: indexers.IndexAPIExportByVirtualResourceIdentitiesAndGRs,
				},
			)
			indexers.AddIfNotPresentOrDie(
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.APIExportByIdentity:                        indexers.IndexAPIExportByIdentity,
					indexers.ByLogicalClusterPathAndName:                indexers.IndexByLogicalClusterPathAndName,
					indexers.APIExportByVirtualResourceIdentitiesAndGRs: indexers.IndexAPIExportByVirtualResourceIdentitiesAndGRs,
				},
			)

			// APIBinding indexers.
			indexers.AddIfNotPresentOrDie(localKcpInformers.Apis().V1alpha2().APIBindings().Informer().GetIndexer(), cache.Indexers{
				indexers.APIBindingsByAPIExport:               indexers.IndexAPIBindingByAPIExport,
				indexers.APIBindingByIdentityAndGroupResource: indexers.IndexAPIBindingByIdentityGroupResource,
			})

			// Wait for caches to be synced.

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

				return nil
			}); err != nil {
				return nil, err
			}

			return &singleResourceAPIDefinitionSetProvider{
				localKcpInformers:  localKcpInformers,
				globalKcpInformers: globalKcpInformers,

				getLogicalCluster: func(cluster logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
					return localKcpInformers.Core().V1alpha1().LogicalClusters().Cluster(cluster).Lister().Get(name)
				},

				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return localKcpInformers.Apis().V1alpha2().APIBindings().Cluster(cluster).Lister().Get(name)
				},

				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
						apisv1alpha2.Resource("apiexports"),
						localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						path,
						name,
					)
				},

				getAPIResourceSchema: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIResourceSchema, apisv1alpha1listers.APIResourceSchemaLister](localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(), globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister()),

				getCachedResource: informer.NewScopedGetterWithFallback[*cachev1alpha1.CachedResource, cachev1alpha1listers.CachedResourceLister](localKcpInformers.Cache().V1alpha1().CachedResources().Lister(), globalKcpInformers.Cache().V1alpha1().CachedResources().Lister()),

				getAPIExportsByVRIdentityAndGR: func(vrIdentity string, gr schema.GroupResource) ([]*apisv1alpha2.APIExport, error) {
					return indexers.ByIndex[*apisv1alpha2.APIExport](globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(), indexers.APIExportByVirtualResourceIdentitiesAndGRs, indexers.VirtualResourceIdentityAndGRKey(vrIdentity, gr))
				},

				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				storageProvider: func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, cr *cachev1alpha1.CachedResource) (apiserver.RestProviderFunc, error) {
					return forwardingregistry.ProvideReadOnlyRestStorage(
						ctx,
						dynamicClusterClientFunc,
						withUnwrapping(apiResourceSchema, version, localKcpInformers, globalKcpInformers, cr),
						nil,
					)
				},
			}, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: replication.VirtualWorkspaceName, VirtualWorkspace: cachedResourceContent},
	}, nil
}

func listAPIBindingsByAPIExport(apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer, export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
		if err != nil {
			return nil, err
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
	if err != nil {
		return nil, err
	}
	keys.Insert(clusterKeys...)

	bindings := make([]*apisv1alpha2.APIBinding, 0, keys.Len())
	for _, key := range sets.List[string](keys) {
		binding, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		} else if !exists {
			utilruntime.HandleError(fmt.Errorf("APIBinding %q does not exist", key))
			continue
		}
		bindings = append(bindings, binding.(*apisv1alpha2.APIBinding))
	}
	return bindings, nil
}

func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	key dynamiccontext.APIDomainKey,
	logicalPath string, accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/replication/root:org:ws/<cachedresource-name>/clusters/<resource cluster>/api/v1/configmaps
	//                       └──────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	cachedResourceClusterName, cachedResourceName := parts[0], parts[1]
	if cachedResourceClusterName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}
	if cachedResourceName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 2 {
		realPath += parts[2]
	}

	//  /services/replication/root:org:ws/<cachedresource-name>/clusters/<resource cluster>/api/v1/configmaps
	//                     ┌───────────────────────────────────┘
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

	key = apidomainkey.New(logicalcluster.Name(cachedResourceClusterName), cachedResourceName)
	return cluster, key, strings.TrimSuffix(urlPath, realPath), true
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

var _ apidefinition.APIDefinitionSetGetter = &singleResourceAPIDefinitionSetProvider{}

type singleResourceAPIDefinitionSetProvider struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient kcpdynamic.ClusterInterface
	storageProvider      func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, cr *cachev1alpha1.CachedResource) (apiserver.RestProviderFunc, error)

	localKcpInformers  kcpinformers.SharedInformerFactory
	globalKcpInformers kcpinformers.SharedInformerFactory

	getLogicalCluster              func(cluster logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding                  func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExportByPath             func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getCachedResource              func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResource, error)
	getAPIExportsByVRIdentityAndGR func(vrIdentity string, gr schema.GroupResource) ([]*apisv1alpha2.APIExport, error)
	getAPIResourceSchema           func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	// TODO: consider making this into a controller.

	parsedKey, err := apidomainkey.Parse(key)
	if err != nil {
		return nil, false, err
	}

	cachedResource, err := a.getCachedResource(parsedKey.CachedResourceCluster, parsedKey.CachedResourceName)
	if err != nil {
		return nil, false, err
	}
	if !conditions.IsTrue(cachedResource, cachev1alpha1.CachedResourceValid) {
		return nil, false, fmt.Errorf("CachedResource %s|%s not ready", parsedKey.CachedResourceCluster, parsedKey.CachedResourceName)
	}

	wrappedGVR := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)

	exports, err := a.getAPIExportsByVRIdentityAndGR(cachedResource.Status.IdentityHash, wrappedGVR.GroupResource())
	if err != nil {
		return nil, false, err
	}
	if len(exports) == 0 {
		return nil, false, fmt.Errorf("CachedResource %s|%s doesn't have an APIExport", parsedKey.CachedResourceCluster, parsedKey.CachedResourceName)
	}
	// There might be multiple exports with the same identity hash all exporting the same GR.
	// We can just pick one. To make it deterministic, we sort the exports.
	sort.Slice(exports, func(i, j int) bool {
		a := exports[i]
		b := exports[j]
		return a.Name < b.Name && logicalcluster.From(a).String() < logicalcluster.From(b).String()
	})
	export := exports[0]

	var wrappedSch *apisv1alpha1.APIResourceSchema
	for _, res := range export.Spec.Resources {
		if res.Group != wrappedGVR.Group || res.Name != wrappedGVR.Resource {
			continue
		}
		if res.Storage.Virtual == nil {
			continue
		}
		if res.Storage.Virtual.IdentityHash != cachedResource.Status.IdentityHash {
			continue
		}

		sch, err := a.getAPIResourceSchema(logicalcluster.From(export), res.Schema)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get APIResourceSchema: %v", err)
		}

		if sch.Spec.Group != wrappedGVR.Group {
			continue
		}
		if sch.Spec.Names.Plural != wrappedGVR.Resource {
			continue
		}

		hasVersion := false
		for _, schVersion := range sch.Spec.Versions {
			if schVersion.Name != wrappedGVR.Version {
				continue
			}
			if !schVersion.Served {
				continue
			}

			hasVersion = true
			break
		}

		if hasVersion {
			wrappedSch = sch
			break
		}
	}

	if wrappedSch == nil {
		return nil, false, fmt.Errorf("failed to get schema for wrapped object in CachedResource %s|%s: missing schema", parsedKey.CachedResourceCluster, parsedKey.CachedResourceName)
	}

	clientFactory := func(ctx context.Context) (kcpdynamic.ClusterInterface, error) {
		return a.dynamicClusterClient, nil
	}

	restProvider, err := a.storageProvider(ctx, clientFactory, wrappedSch, wrappedGVR.Version, cachedResource)
	if err != nil {
		return nil, false, err
	}

	apiDefinition, err := apiserver.CreateServingInfoFor(
		a.config,
		wrappedSch,
		wrappedGVR.Version,
		restProvider,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create serving info: %w", err)
	}

	return apidefinition.APIDefinitionSet{
		wrappedGVR: apiDefinition,
	}, true, nil
}
