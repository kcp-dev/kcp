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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	authenticationv1 "k8s.io/api/authentication/v1"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	crdlisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/transport"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdgroups"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
)

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	dynamicClusterClient dynamic.ClusterInterface,
	kubeClusterClient kubernetes.ClusterInterface,
	wildcardApiExtensionsInformers apiextensionsinformers.SharedInformerFactory,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) map[string]framework.VirtualWorkspace {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	filterReadyCh := make(chan struct{})
	filterName := initializingworkspaces.VirtualWorkspaceName + "-filtered"
	filtered := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", requestContext
			}

			if !cluster.Wildcard {
				// this virtual workspace requires that a wildcard be provided
				return false, "", requestContext
			}

			completedContext = genericapirequest.WithCluster(requestContext, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		},
		Authorizer: newAuthorizer(kubeClusterClient),
		Ready: func() error {
			select {
			case <-filterReadyCh:
				return nil
			default:
				return fmt.Errorf("%s virtual workspace controllers are not started", filterName)
			}
		},
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			retriever := &apiSetRetriever{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				crdLister:            wildcardApiExtensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Lister(),
			}

			if err := mainConfig.AddPostStartHook(filterName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(filterReadyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"customresourcedefinitions": wildcardApiExtensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return retriever, nil
		},
	}

	forwardReadyCh := make(chan struct{})
	forwardName := initializingworkspaces.VirtualWorkspaceName + "-forwarded"
	forwarding := &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", context
			}

			if cluster.Wildcard {
				// this virtual workspace requires that a specific cluster be provided
				return false, "", context
			}

			// in this case since we're proxying and not consuming this request we *do not* want to strip
			// the cluster prefix
			prefixToStrip = strings.TrimSuffix(prefixToStrip, cluster.Name.Path())

			completedContext = genericapirequest.WithCluster(context, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-forwardReadyCh:
				return nil
			default:
				return fmt.Errorf("%s virtual workspace controllers are not started", forwardName)
			}
		}),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			if err := rootAPIServerConfig.AddPostStartHook(forwardName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(forwardReadyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"clusterworkspaces": wildcardKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			forwardedHost, err := url.Parse(cfg.Host)
			if err != nil {
				return nil, err
			}

			lister := wildcardKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces().Lister()
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				cluster, err := genericapirequest.ClusterNameFrom(request.Context())
				if err != nil {
					http.Error(writer, fmt.Sprintf("could not determine cluster for request: %v", err), http.StatusInternalServerError)
					return
				}
				parent, name := cluster.Split()
				clusterWorkspace, err := lister.Get(clusters.ToClusterAwareKey(parent, name))
				if err != nil {
					http.Error(writer, fmt.Sprintf("could not find cluster %q: %v", parent, err), http.StatusInternalServerError)
					return
				}

				initializer := tenancyv1alpha1.ClusterWorkspaceInitializer(dynamiccontext.APIDomainKeyFrom(request.Context()))
				if clusterWorkspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing || !initialization.InitializerPresent(initializer, clusterWorkspace.Status.Initializers) {
					http.Error(writer, fmt.Sprintf("initializer %q cannot access this workspace %v %v", initializer, clusterWorkspace.Status.Phase, clusterWorkspace.Status.Initializers), http.StatusForbidden)
					return
				}

				rawInfo, ok := clusterWorkspace.Annotations[tenancyv1alpha1.ClusterWorkspaceOwnerAnnotationKey]
				if !ok {
					http.Error(writer, fmt.Sprintf("cluster %q had no user recorded", parent), http.StatusInternalServerError)
					return
				}
				var info authenticationv1.UserInfo
				if err := json.Unmarshal([]byte(rawInfo), &info); err != nil {
					http.Error(writer, fmt.Sprintf("could not unmarshal user info for cluster %q: %v", parent, err), http.StatusInternalServerError)
					return
				}
				extra := map[string][]string{}
				for k, v := range info.Extra {
					extra[k] = v
				}

				thisCfg := rest.CopyConfig(cfg)
				thisCfg.Impersonate = rest.ImpersonationConfig{
					UserName: info.Username,
					UID:      info.UID,
					Groups:   info.Groups,
					Extra:    extra,
				}
				authenticatingTransport, err := rest.TransportFor(thisCfg)
				if err != nil {
					http.Error(writer, fmt.Sprintf("could create round-tripper: %v", err), http.StatusInternalServerError)
					return
				}
				proxy := &httputil.ReverseProxy{
					Director: func(request *http.Request) {
						for _, header := range []string{
							"Authorization",
							transport.ImpersonateUserHeader,
							transport.ImpersonateUIDHeader,
							transport.ImpersonateGroupHeader,
						} {
							request.Header.Del(header)
						}
						for key := range request.Header {
							if strings.HasPrefix(key, transport.ImpersonateUserExtraHeaderPrefix) {
								request.Header.Del(key)
							}
						}
						request.URL.Scheme = forwardedHost.Scheme
						request.URL.Host = forwardedHost.Host
					},
					Transport: authenticatingTransport,
				}
				proxy.ServeHTTP(writer, request)
			}), nil
		}),
	}

	return map[string]framework.VirtualWorkspace{
		filterName:  filtered,
		forwardName: forwarding,
	}
}

func digestUrl(urlPath, rootPathPrefix string) (genericapirequest.Cluster, dynamiccontext.APIDomainKey, string, bool) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.dev/v1alpha1/workloadclusters
	//                                  └───────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	parts := strings.SplitN(withoutRootPathPrefix, "/", 2)
	if len(parts) < 2 {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false
	}

	initializerName := parts[0]
	if initializerName == "" {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false
	}

	realPath := "/" + parts[1]

	//  /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.dev/v1alpha1/workloadclusters
	//                  ┌─────────────────────────────┘
	// We are now here: ┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false // don't accept
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	clusterName := logicalcluster.New(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	return genericapirequest.Cluster{Name: clusterName, Wildcard: clusterName == logicalcluster.Wildcard}, dynamiccontext.APIDomainKey(initializerName), strings.TrimSuffix(urlPath, realPath), true
}

type apiSetRetriever struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient dynamic.ClusterInterface
	crdLister            crdlisters.CustomResourceDefinitionLister
}

func (a *apiSetRetriever) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	crd, err := a.crdLister.Get(
		clusters.ToClusterAwareKey(
			logicalcluster.New(reservedcrdgroups.SystemCRDLogicalClusterName),
			"clusterworkspaces.tenancy.kcp.dev",
		),
	)
	if err != nil {
		return nil, false, err
	}

	apiResourceSchema := &apisv1alpha1.APIResourceSchema{
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: crd.Spec.Group,
			Names: crd.Spec.Names,
			Scope: crd.Spec.Scope,
		},
	}

	for i := range crd.Spec.Versions {
		crdVersion := crd.Spec.Versions[i]

		apiResourceVersion := apisv1alpha1.APIResourceVersion{
			Name:                     crdVersion.Name,
			Served:                   crdVersion.Served,
			Storage:                  crdVersion.Storage,
			Deprecated:               crdVersion.Deprecated,
			DeprecationWarning:       crdVersion.DeprecationWarning,
			AdditionalPrinterColumns: crdVersion.AdditionalPrinterColumns,
		}

		if crdVersion.Schema != nil && crdVersion.Schema.OpenAPIV3Schema != nil {
			schemaBytes, err := json.Marshal(crdVersion.Schema.OpenAPIV3Schema)
			if err != nil {
				return nil, false, fmt.Errorf("error converting version %q schema: %w", crdVersion.Name, err)
			}
			apiResourceVersion.Schema.Raw = schemaBytes
		}

		// we never expose subresources for this virtual workspace

		apiResourceSchema.Spec.Versions = append(apiResourceSchema.Spec.Versions, apiResourceVersion)
	}

	apis = make(apidefinition.APIDefinitionSet, len(apiResourceSchema.Spec.Versions))
	for _, version := range apiResourceSchema.Spec.Versions {
		gvr := schema.GroupVersionResource{
			Group:    apiResourceSchema.Spec.Group,
			Version:  version.Name,
			Resource: apiResourceSchema.Spec.Names.Plural,
		}
		apiDefinition, err := apiserver.CreateServingInfoFor(
			a.config,
			apiResourceSchema,
			version.Name,
			provideForwardingRestStorage(ctx, a.dynamicClusterClient, tenancyv1alpha1.ClusterWorkspaceInitializer(key)),
		)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create serving info: %w", err)
		}
		apis[gvr] = apiDefinition
	}

	return apis, len(apis) > 0, nil
}

var _ apidefinition.APIDefinitionSetGetter = &apiSetRetriever{}

func newAuthorizer(client kubernetes.ClusterInterface) authorizer.AuthorizerFunc {
	return func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		workspace, name, err := initialization.TypeFrom(tenancyv1alpha1.ClusterWorkspaceInitializer(dynamiccontext.APIDomainKeyFrom(ctx)))
		if err != nil {
			klog.V(2).Info(err)
			return authorizer.DecisionNoOpinion, "unable to determine initializer", fmt.Errorf("access not permitted")
		}

		authz, err := delegated.NewDelegatedAuthorizer(workspace, client)
		if err != nil {
			return authorizer.DecisionNoOpinion, "error", err
		}

		SARAttributes := authorizer.AttributesRecord{
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
			User:            attr.GetUser(),
			Verb:            "initialize",
			Name:            name,
			Resource:        "clusterworkspacetypes",
			ResourceRequest: true,
		}

		return authz.Authorize(ctx, SARAttributes)
	}
}
