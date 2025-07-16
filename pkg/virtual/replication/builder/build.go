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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	cachedresourcesreplication "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
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
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	kcpClusterClient kcpclientset.ClusterInterface,
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

			if targetCluster.Wildcard {
				return false, "", requestContext
			}

			parsedKey, err := apidomainkey.Parse(apiDomain)
			if err != nil {
				return false, "", requestContext
			}

			if targetCluster.Name != parsedKey.CachedResourceCluster {
				return false, "", requestContext
			}

			// We only accept requests for CachedResource's local cluster.

			completedContext = genericapirequest.WithCluster(requestContext, targetCluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuth(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("replication virtual workspace controllers are not started")
			}
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			if err := mainConfig.AddPostStartHook(replication.VirtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				indexers.AddIfNotPresentOrDie(
					globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer().GetIndexer(),
					cache.Indexers{
						cachedresourcesreplication.ByGVRAndLogicalClusterAndNamespace: cachedresourcesreplication.IndexByGVRAndLogicalClusterAndNamespace,
					},
				)
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

				for name, informer := range map[string]cache.SharedIndexInformer{
					"cachedresources":    globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer(),
					"apiexports":         globalKcpInformers.Apis().V1alpha2().APIExports().Informer(),
					"apiresourceschemas": globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "informer not synced")
						return nil
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return &singleResourceAPIDefinitionSetProvider{
				localKcpInformers: localKcpInformers,
				kcpClusterClient:  kcpClusterClient,

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

				getAPIResourceSchemaByName: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Cluster(cluster).Lister().Get(name)
				},

				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				storageProvider: func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string) (apiserver.RestProviderFunc, error) {
					return forwardingregistry.ProvideReadOnlyRestStorage(
						ctx,
						dynamicClusterClientFunc,
						withUnwrapping(apiResourceSchema, version, globalKcpInformers),
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

func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	key dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
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

func newAuth(deepSARClient kcpkubernetesclientset.ClusterInterface) authorizer.Authorizer {
	wrappedResourceAuthorizer := replicationauthorizer.NewWrappedResourceAuthorizer(deepSARClient)
	wrappedResourceAuthorizer = authorization.NewDecorator("virtual.replication.wrappedresource.authorization.kcp.io", wrappedResourceAuthorizer).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	return wrappedResourceAuthorizer
}

var _ apidefinition.APIDefinitionSetGetter = &singleResourceAPIDefinitionSetProvider{}

type singleResourceAPIDefinitionSetProvider struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient kcpdynamic.ClusterInterface
	storageProvider      func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, sch *apisv1alpha1.APIResourceSchema, version string) (apiserver.RestProviderFunc, error)

	kcpClusterClient  kcpclientset.ClusterInterface
	localKcpInformers kcpinformers.SharedInformerFactory

	getLogicalCluster          func(cluster logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding              func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExportByPath         func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchemaByName func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func getResourceBindingsAnnJSON(lc *corev1alpha1.LogicalCluster) string {
	const jsonEmptyObj = "{}"

	if lc == nil {
		return jsonEmptyObj
	}

	ann := lc.Annotations[apibinding.ResourceBindingsAnnotationKey]
	if ann == "" {
		ann = jsonEmptyObj
	}

	return ann
}

func (a *singleResourceAPIDefinitionSetProvider) getAPIResourceSchema(
	ctx context.Context,
	clusterName logicalcluster.Name,
	gvr schema.GroupVersionResource,
) (*apisv1alpha1.APIResourceSchema, error) {
	if gvr.Group == "" {
		// Assume built-in types.
		return builtin.GetBuiltInAPISchema(apisv1alpha1.GroupResource{Group: "", Resource: gvr.Resource})
	}

	lc, err := a.getLogicalCluster(clusterName, "cluster")
	if err != nil {
		return nil, err
	}
	resBindingsAnnStr := getResourceBindingsAnnJSON(lc)
	resBindingsAnn, err := apibinding.UnmarshalResourceBindingsAnnotation(resBindingsAnnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse annotation on LogicalCluster %s|%s: %v", clusterName, "cluster", err)
	}

	bindingName := ""
	for gr, v := range resBindingsAnn {
		if v.CRD {
			continue
		}
		if gr == gvr.GroupResource().String() {
			bindingName = v.Name
		}
	}

	if bindingName == "" {
		return nil, fmt.Errorf("no binding for %s found in workspace %s", gvr.GroupResource().String(), clusterName)
	}

	apiBinding, err := a.getAPIBinding(clusterName, bindingName)
	if err != nil {
		return nil, fmt.Errorf("failed to get APIBinding %s|%s", bindingName, clusterName)
	}

	apiExport, err := a.getAPIExportByPath(logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path), apiBinding.Spec.Reference.Export.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get APIExport %s|%s referenced by APIBinding %s|%s: %v",
			apiBinding.Spec.Reference.Export.Path, apiBinding.Spec.Reference.Export.Name,
			clusterName, bindingName, err,
		)
	}
	apiExportClusterName := logicalcluster.From(apiExport)

	schName := ""
	for _, exportResource := range apiExport.Spec.Resources {
		if exportResource.Group == gvr.Group && exportResource.Name == gvr.Resource {
			schName = exportResource.Schema
		}
	}

	return a.getAPIResourceSchemaByName(apiExportClusterName, schName)
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	parsedKey, err := apidomainkey.Parse(key)
	if err != nil {
		return nil, false, err
	}

	clientFactory := func(ctx context.Context) (kcpdynamic.ClusterInterface, error) {
		return a.dynamicClusterClient, nil
	}

	cachedResource, err := a.kcpClusterClient.CacheV1alpha1().CachedResources().Cluster(parsedKey.CachedResourceCluster.Path()).
		Get(ctx, parsedKey.CachedResourceName, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}

	wrappedGVR := schema.GroupVersionResource(cachedResource.Spec.GroupVersionResource)
	wrappedSch, err := a.getAPIResourceSchema(ctx, parsedKey.CachedResourceCluster, wrappedGVR)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get schema for wrapped object in CachedResource %s|%s: %v", parsedKey.CachedResourceCluster, parsedKey.CachedResourceName, err)
	}

	restProvider, err := a.storageProvider(ctx, clientFactory, wrappedSch, wrappedGVR.Version)
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
