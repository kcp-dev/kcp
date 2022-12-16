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
	"path"
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	authenticationv1 "k8s.io/api/authentication/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/transport"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	rootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
)

func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	logicalClusterResource := apisv1alpha1.APIResourceSchema{}
	if err := rootphase0.Unmarshal("apiresourceschema-logicalclusters.core.kcp.dev.yaml", &logicalClusterResource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal logicalclusters resource: %w", err)
	}
	bs, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
		Type:                   "object",
		XPreserveUnknownFields: pointer.BoolPtr(true),
	})
	if err != nil {
		return nil, err
	}
	for i := range logicalClusterResource.Spec.Versions {
		v := &logicalClusterResource.Spec.Versions[i]
		v.Schema.Raw = bs // wipe schemas. We don't want validation here.
	}

	getTenancyIdentity := func() (string, error) {
		export, err := wildcardKcpInformers.Apis().V1alpha1().APIExports().Lister().Cluster(tenancyv1alpha1.RootCluster).Get("tenancy.kcp.dev")
		if err != nil {
			return "", err
		}
		if export.Status.IdentityHash == "" {
			return "", errors.New("identity hash is empty")
		}
		return export.Status.IdentityHash, nil
	}

	wildcardLogicalClustersName := initializingworkspaces.VirtualWorkspaceName + "-wildcard-logicalclusters"
	wildcardLogicalClusters := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
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
		}),
		Authorizer: newAuthorizer(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			return nil
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &singleResourceAPIDefinitionSetProvider{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				exposeSubresources:   false,
				resource:             &logicalClusterResource,
				storageProvider:      provideFilteredClusterWorkspacesReadOnlyRestStorage(getTenancyIdentity),
			}, nil
		},
	}

	LogicalClustersName := initializingworkspaces.VirtualWorkspaceName + "-logicalclusters"
	logicalClusters := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			if cluster.Wildcard {
				// this virtual workspace requires that a specific cluster be provided
				return false, "", ctx
			}

			// this delegating server only works for logicalclusters.core.kcp.dev
			if resourceURL := strings.TrimPrefix(urlPath, prefixToStrip); !isLogicalClusterRequest(resourceURL) {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			return nil
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &singleResourceAPIDefinitionSetProvider{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				exposeSubresources:   true,
				resource:             &logicalClusterResource,
				storageProvider:      provideDelegatingClusterWorkspacesRestStorage(getTenancyIdentity),
			}, nil
		},
	}

	workspaceContentReadyCh := make(chan struct{})
	workspaceContentName := initializingworkspaces.VirtualWorkspaceName + "-workspace-content"
	workspaceContent := &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", context
			}

			if cluster.Wildcard {
				// this virtual workspace requires that a specific cluster be provided
				return false, "", context
			}

			// this proxying server does not handle requests for thisworkspaces.tenancy.kcp.dev
			if resourceURL := strings.TrimPrefix(urlPath, prefixToStrip); isLogicalClusterRequest(resourceURL) {
				return false, "", context
			}

			// in this case since we're proxying and not consuming this request we *do not* want to strip
			// the cluster prefix
			prefixToStrip = strings.TrimSuffix(prefixToStrip, cluster.Name.Path().RequestPath())

			completedContext = genericapirequest.WithCluster(context, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),
		Authorizer: newAuthorizer(kubeClusterClient),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-workspaceContentReadyCh:
				return nil
			default:
				return fmt.Errorf("%s virtual workspace controllers are not started", workspaceContentName)
			}
		}),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			if err := rootAPIServerConfig.AddPostStartHook(workspaceContentName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(workspaceContentReadyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"logicalclusters": wildcardKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						klog.Errorf("informer not synced")
						return nil
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

			lister := wildcardKcpInformers.Core().V1alpha1().LogicalClusters().Lister()
			return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				cluster, err := genericapirequest.ClusterNameFrom(request.Context())
				if err != nil {
					http.Error(writer, fmt.Sprintf("could not determine cluster for request: %v", err), http.StatusInternalServerError)
					return
				}
				logicalCluster, err := lister.Cluster(cluster).Get(corev1alpha1.LogicalClusterName)
				if err != nil {
					http.Error(writer, fmt.Sprintf("error getting logicalcluster %s|%s: %v", cluster, corev1alpha1.LogicalClusterName, err), http.StatusInternalServerError)
					return
				}

				initializer := corev1alpha1.LogicalClusterInitializer(dynamiccontext.APIDomainKeyFrom(request.Context()))
				if logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing || !initialization.InitializerPresent(initializer, logicalCluster.Status.Initializers) {
					http.Error(writer, fmt.Sprintf("initializer %q cannot access this workspace", initializer), http.StatusForbidden)
					return
				}

				rawInfo, ok := logicalCluster.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]
				if !ok {
					http.Error(writer, fmt.Sprintf("LogicalCluster %s|%s had no user recorded", cluster, corev1alpha1.LogicalClusterName), http.StatusInternalServerError)
					return
				}
				var info authenticationv1.UserInfo
				if err := json.Unmarshal([]byte(rawInfo), &info); err != nil {
					http.Error(writer, fmt.Sprintf("could not unmarshal user info for cluster %q: %v", cluster, err), http.StatusInternalServerError)
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

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: wildcardLogicalClustersName, VirtualWorkspace: wildcardLogicalClusters},
		{Name: LogicalClustersName, VirtualWorkspace: logicalClusters},
		{Name: workspaceContentName, VirtualWorkspace: workspaceContent},
	}, nil
}

var resolver = requestinfo.NewFactory()

func isLogicalClusterRequest(path string) bool {
	info, err := resolver.NewRequestInfo(&http.Request{URL: &url.URL{Path: path}})
	if err != nil {
		return false
	}
	return info.IsResourceRequest && info.APIGroup == corev1alpha1.SchemeGroupVersion.Group && info.Resource == "logicalclusters"
}

func digestUrl(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	key dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.dev/v1alpha1/synctargets
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

	//  /services/initializingworkspaces/<initializer>/clusters/<something>/apis/workload.kcp.dev/v1alpha1/synctargets
	//                  ┌─────────────────────────────┘
	// We are now here: ┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, dynamiccontext.APIDomainKey(""), "", false // don't accept
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

	return cluster, dynamiccontext.APIDomainKey(initializerName), strings.TrimSuffix(urlPath, realPath), true
}

// URLFor returns the absolute path for the specified initializer.
func URLFor(initializerName corev1alpha1.LogicalClusterInitializer) string {
	// TODO(ncdc): make /services hard-coded everywhere instead of configurable.
	return path.Join("/services", initializingworkspaces.VirtualWorkspaceName, string(initializerName))
}

type singleResourceAPIDefinitionSetProvider struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient kcpdynamic.ClusterInterface
	resource             *apisv1alpha1.APIResourceSchema
	exposeSubresources   bool
	storageProvider      func(ctx context.Context, clusterClient kcpdynamic.ClusterInterface, initializer corev1alpha1.LogicalClusterInitializer) (apiserver.RestProviderFunc, error)
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	restProvider, err := a.storageProvider(ctx, a.dynamicClusterClient, corev1alpha1.LogicalClusterInitializer(key))
	if err != nil {
		return nil, false, err
	}

	apiDefinition, err := apiserver.CreateServingInfoFor(
		a.config,
		a.resource,
		corev1alpha1.SchemeGroupVersion.Version,
		restProvider,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create serving info: %w", err)
	}

	apis = apidefinition.APIDefinitionSet{
		schema.GroupVersionResource{
			Group:    corev1alpha1.SchemeGroupVersion.Group,
			Version:  corev1alpha1.SchemeGroupVersion.Version,
			Resource: "logicalclusters",
		}: apiDefinition,
	}

	return apis, len(apis) > 0, nil
}

var _ apidefinition.APIDefinitionSetGetter = &singleResourceAPIDefinitionSetProvider{}

func newAuthorizer(client kcpkubernetesclientset.ClusterInterface) authorizer.AuthorizerFunc {
	return func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		clusterName, name, err := initialization.TypeFrom(corev1alpha1.LogicalClusterInitializer(dynamiccontext.APIDomainKeyFrom(ctx)))
		if err != nil {
			klog.V(2).Info(err)
			return authorizer.DecisionNoOpinion, "unable to determine initializer", fmt.Errorf("access not permitted")
		}

		authz, err := delegated.NewDelegatedAuthorizer(clusterName, client)
		if err != nil {
			return authorizer.DecisionNoOpinion, "error", err
		}

		SARAttributes := authorizer.AttributesRecord{
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
			User:            attr.GetUser(),
			Verb:            "initialize",
			Name:            name,
			Resource:        "workspacetypes",
			ResourceRequest: true,
		}

		return authz.Authorize(ctx, SARAttributes)
	}
}
