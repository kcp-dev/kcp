/*
Copyright 2022 The KCP Authors.

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
	"net/http"
	"net/url"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

const VirtualWorkspaceName string = "apiexport"

func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kubernetesclient.ClusterInterface,
	dynamicClusterClient dynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	apiBindingsName := VirtualWorkspaceName + "-apibindings"
	apiBindings := &virtualdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			if resourceURL := strings.TrimPrefix(urlPath, prefixToStrip); !isAPIBindingRequest(resourceURL) {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("apiexport virtual workspace controllers are not started")
			}
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &apiSetRetriever{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				exposeSubresources:   true,
				resource:             schemas.ApisKcpDevSchemas["apibindings"],
				storageProvider:      provideAPIExportFilteredRestStorage,
			}, nil
		},
	}

	boundOrClaimedWorkspaceContent := &virtualdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("apiexport virtual workspace controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			apiReconciler, err := apireconciler.NewAPIReconciler(
				kcpClusterClient,
				wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
				wildcardKcpInformers.Apis().V1alpha1().APIExports(),
				func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string, optionalLabelRequirements labels.Requirements) (apidefinition.APIDefinition, error) {
					ctx, cancelFn := context.WithCancel(context.Background())

					var wrapper forwardingregistry.StorageWrapper = nil
					if len(optionalLabelRequirements) > 0 {
						wrapper = forwardingregistry.WithLabelSelector(func(_ context.Context) labels.Requirements {
							return optionalLabelRequirements
						})
					}

					storageBuilder := provideDelegatingRestStorage(ctx, dynamicClusterClient, identityHash, wrapper)
					def, err := apiserver.CreateServingInfoFor(mainConfig, apiResourceSchema, version, storageBuilder)
					if err != nil {
						cancelFn()
						return nil, err
					}
					return &apiDefinitionWithCancel{
						APIDefinition: def,
						cancelFn:      cancelFn,
					}, nil
				},
			)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook(apireconciler.ControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"apiresourceschemas": wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
					"apiexports":         wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						klog.Errorf("informer not synced")
						return nil
					}
				}

				go apiReconciler.Start(goContext(hookContext))
				return nil
			}); err != nil {
				return nil, err
			}

			return apiReconciler, nil
		},
		Authorizer: newAuthorizer(kubeClusterClient),
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: VirtualWorkspaceName, VirtualWorkspace: boundOrClaimedWorkspaceContent}, // this must come first because a claim will show all bindings, not only those for the export
		{Name: apiBindingsName, VirtualWorkspace: apiBindings},
	}, nil
}

var resolver = requestinfo.NewFactory()

func isAPIBindingRequest(path string) bool {
	info, err := resolver.NewRequestInfo(&http.Request{URL: &url.URL{Path: path}})
	if err != nil {
		return false
	}
	return info.IsResourceRequest && info.APIGroup == apisv1alpha1.SchemeGroupVersion.Group && info.Resource == "apibindings"
}

func digestUrl(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     └────────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	apiExportClusterName, apiExportName := parts[0], parts[1]
	if apiExportClusterName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}
	if apiExportName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 2 {
		realPath += parts[2]
	}

	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     ┌────────────────────────────┘
	// We are now here: ───┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	clusterName := logicalcluster.New(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	key := fmt.Sprintf("%s/%s", apiExportClusterName, apiExportName)
	return genericapirequest.Cluster{Name: clusterName, Wildcard: clusterName == logicalcluster.Wildcard}, dynamiccontext.APIDomainKey(key), strings.TrimSuffix(urlPath, realPath), true
}

type apiSetRetriever struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient dynamic.ClusterInterface
	resource             *apisv1alpha1.APIResourceSchema
	exposeSubresources   bool
	storageProvider      func(ctx context.Context, clusterClient dynamic.ClusterInterface, exportCluster logicalcluster.Name, exportName string) (apiserver.RestProviderFunc, error)
}

func (a *apiSetRetriever) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	comps := strings.SplitN(string(key), "/", 2)
	if len(comps) != 2 {
		return nil, false, fmt.Errorf("invalid key: %s", key)
	}
	restProvider, err := a.storageProvider(ctx, a.dynamicClusterClient, logicalcluster.New(comps[0]), comps[1])
	if err != nil {
		return nil, false, err
	}

	apiDefinition, err := apiserver.CreateServingInfoFor(
		a.config,
		a.resource,
		apisv1alpha1.SchemeGroupVersion.Version,
		restProvider,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create serving info: %w", err)
	}

	apis = apidefinition.APIDefinitionSet{
		schema.GroupVersionResource{
			Group:    apisv1alpha1.SchemeGroupVersion.Group,
			Version:  apisv1alpha1.SchemeGroupVersion.Version,
			Resource: "apibindings",
		}: apiDefinition,
	}

	return apis, len(apis) > 0, nil
}

var _ apidefinition.APIDefinitionSetGetter = &apiSetRetriever{}

func newAuthorizer(client kubernetesclient.ClusterInterface) authorizer.AuthorizerFunc {
	return func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
		parts := strings.Split(string(apiDomainKey), "/")
		if len(parts) < 2 {
			return authorizer.DecisionNoOpinion, "unable to determine api export", fmt.Errorf("access not permitted")
		}

		apiExportCluster, apiExportName := parts[0], parts[1]
		authz, err := delegated.NewDelegatedAuthorizer(logicalcluster.New(apiExportCluster), client)
		if err != nil {
			return authorizer.DecisionNoOpinion, "error", err
		}

		SARAttributes := authorizer.AttributesRecord{
			APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
			User:            attr.GetUser(),
			Verb:            attr.GetVerb(),
			Name:            apiExportName,
			Resource:        "apiexports",
			ResourceRequest: true,
			Subresource:     "content",
		}

		return authz.Authorize(ctx, SARAttributes)
	}
}

// apiDefinitionWithCancel calls the cancelFn on tear-down.
type apiDefinitionWithCancel struct {
	apidefinition.APIDefinition
	cancelFn func()
}

func (d *apiDefinitionWithCancel) TearDown() {
	d.cancelFn()
	d.APIDefinition.TearDown()
}

func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}
