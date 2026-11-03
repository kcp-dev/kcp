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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/tenancy/termination"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/handler"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	rootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	virtualshared "github.com/kcp-dev/kcp/pkg/virtual/shared"
	"github.com/kcp-dev/kcp/pkg/virtual/terminatingworkspaces"
)

const (
	wildcardLogicalClustersName = terminatingworkspaces.VirtualWorkspaceName + "-wildcard-logicalclusters"
	logicalClustersName         = terminatingworkspaces.VirtualWorkspaceName + "-logicalclusters"
	workspaceContentName        = terminatingworkspaces.VirtualWorkspaceName + "-workspace-content"
)

const terminatorWildcard = "terminator"

// pathPattern returns the mux pattern shared by the virtual workspaces below rootPathPrefix.
func pathPattern(rootPathPrefix string) string {
	return path.Join(rootPathPrefix, "{terminator}/clusters/{cluster}")
}

// BuildVirtualWorkspace builds the terminating-workspaces virtual workspaces.
//
// externalLogicalClusterAdminClient is used for SubjectAccessReview calls
// against the workspacetype's logical cluster. In sharded deployments, the
// workspacetype can live on a different shard than the workspace, so this
// client must be able to reach any cluster (typically routed through the
// front-proxy). When nil, kubeClusterClient is used as a fallback, which only
// works when the workspacetype is co-located with the VW's shard.
func BuildVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kubeClusterClient, externalLogicalClusterAdminClient kcpkubernetesclientset.ClusterInterface,
	wildcardKcpInformers, cachedKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	sarKubeClusterClient := externalLogicalClusterAdminClient
	if sarKubeClusterClient == nil {
		sarKubeClusterClient = kubeClusterClient
	}

	logicalClusterResource := apisv1alpha1.APIResourceSchema{}
	if err := rootphase0.Unmarshal("apiresourceschema-logicalclusters.core.kcp.io.yaml", &logicalClusterResource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal logicalclusters resource: %w", err)
	}
	bs, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
		Type:                   "object",
		XPreserveUnknownFields: ptr.To(true),
	})
	if err != nil {
		return nil, err
	}
	for i := range logicalClusterResource.Spec.Versions {
		v := &logicalClusterResource.Spec.Versions[i]
		v.Schema.Raw = bs // wipe schemas. We don't want validation here.
	}

	// See the matching comment in pkg/virtual/initializingworkspaces/builder/build.go
	// for why DenyCacheTTL is shortened from the 30s default.
	cachingAuthorizer := delegated.NewCachingAuthorizer(sarKubeClusterClient, authorizerWithCache, delegated.CachingOptions{
		Options: delegated.Options{DenyCacheTTL: 5 * time.Second},
	})
	wildcardLogicalClusters := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveWildcardLogicalClusters),
		Authorizer:       cachingAuthorizer,
		ReadyChecker: framework.ReadyFunc(func() error {
			return nil
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &singleResourceAPIDefinitionSetProvider{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				exposeSubresources:   false,
				resource:             &logicalClusterResource,
				storageProvider:      filteredLogicalClusterReadOnlyRestStorage,
			}, nil
		},
	}

	logicalClusters := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveLogicalClusters),
		Authorizer:       cachingAuthorizer,
		ReadyChecker: framework.ReadyFunc(func() error {
			return nil
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &singleResourceAPIDefinitionSetProvider{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				exposeSubresources:   true,
				resource:             &logicalClusterResource,
				storageProvider:      filteredLogicalClusterStatusWriteOnly,
			}, nil
		},
	}

	workspaceContentReadyCh := make(chan struct{})
	workspaceContent := &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveWorkspaceContent),
		Authorizer:       cachingAuthorizer,
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
					"logicalclusters":       wildcardKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
					"workspacetypes":        wildcardKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
					"cached-workspacetypes": cachedKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
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

			forwardedHost, err := url.Parse(cfg.Host)
			if err != nil {
				return nil, err
			}

			lister := wildcardKcpInformers.Core().V1alpha1().LogicalClusters().Lister()
			localWSTIndexer := wildcardKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()
			cachedWSTIndexer := cachedKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()
			indexers.AddIfNotPresentOrDie(localWSTIndexer, cache.Indexers{
				indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
			})
			indexers.AddIfNotPresentOrDie(cachedWSTIndexer, cache.Indexers{
				indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
			})
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

				terminator := corev1alpha1.LogicalClusterTerminator(dynamiccontext.APIDomainKeyFrom(request.Context()))
				if logicalCluster.DeletionTimestamp.IsZero() || !termination.TerminatorPresent(terminator, logicalCluster.Status.Terminators) {
					http.Error(writer, fmt.Sprintf("terminator %q cannot access this workspace", terminator), http.StatusForbidden)
					return
				}

				// Refuse to proxy a request that re-enters "/services/" (another virtual
				// workspace, e.g. /services/admin).
				if authorization.LifecycleProxyPathEscapesWorkspace(request.URL.Path) {
					http.Error(writer, fmt.Sprintf("terminator %q may not access virtual workspace endpoints (/services/...) outside the workspace being terminated", terminator), http.StatusForbidden)
					return
				}

				// Fetch the WorkspaceType the terminator originates from. If it
				// declares terminatorPermissions, evaluate the request in-process
				// and forward with the controller's identity + synthetic group;
				// otherwise fall back to owner impersonation.
				// See resolveTerminatorWorkspaceType for the fail-closed policy.
				wst, lookupErr := resolveTerminatorWorkspaceType(terminator, localWSTIndexer, cachedWSTIndexer)
				if lookupErr != nil {
					klog.FromContext(request.Context()).Error(lookupErr, "WorkspaceType lookup for terminator failed; refusing to proxy", "terminator", terminator)
					http.Error(writer, fmt.Sprintf("terminator %q: WorkspaceType not found in local or cached indexer; refusing to fall back to owner impersonation: %v", terminator, lookupErr), http.StatusServiceUnavailable)
					return
				}

				if wst != nil && len(wst.Spec.TerminatorPermissions) > 0 {
					caller := authorization.RequestUserInfo(request)
					if caller == nil {
						http.Error(writer, "no user information in request context", http.StatusInternalServerError)
						return
					}
					if allowed, reason := authorization.EvaluateLifecyclePermissions(request, caller, wst.Spec.TerminatorPermissions); !allowed {
						http.Error(writer, fmt.Sprintf("terminator %q denied by terminatorPermissions: %s", terminator, reason), http.StatusForbidden)
						return
					}

					thisCfg := rest.CopyConfig(cfg)
					extra := map[string][]string{}
					for k, v := range caller.GetExtra() {
						extra[k] = v
					}
					thisCfg.Impersonate = rest.ImpersonationConfig{
						UserName: caller.GetName(),
						UID:      caller.GetUID(),
						Groups:   append([]string{authorization.TerminatorGroup(terminator)}, caller.GetGroups()...),
						Extra:    extra,
					}
					authenticatingTransport, err := rest.TransportFor(thisCfg)
					if err != nil {
						http.Error(writer, fmt.Sprintf("could not create round-tripper: %v", err), http.StatusInternalServerError)
						return
					}
					virtualshared.ServeProxy(writer, request, forwardedHost, authenticatingTransport)
					return
				}

				// Owner-impersonation fallback (mode 2).
				if logicalCluster.Spec.CreatedBy == nil {
					http.Error(writer, fmt.Sprintf("LogicalCluster %s|%s had no createdBy recorded", cluster, corev1alpha1.LogicalClusterName), http.StatusInternalServerError)
					return
				}
				info := authenticationv1.UserInfo{
					Username: logicalCluster.Spec.CreatedBy.Username,
					UID:      logicalCluster.Spec.CreatedBy.UID,
					Groups:   logicalCluster.Spec.CreatedBy.Groups,
					Extra:    logicalCluster.Spec.CreatedBy.Extra,
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
					http.Error(writer, fmt.Sprintf("could not create round-tripper: %v", err), http.StatusInternalServerError)
					return
				}
				virtualshared.ServeProxy(writer, request, forwardedHost, authenticatingTransport)
			}), nil
		}),
	}

	patterns := []string{pathPattern(rootPathPrefix)}
	return []rootapiserver.NamedVirtualWorkspace{
		{Name: wildcardLogicalClustersName, Patterns: patterns, VirtualWorkspace: wildcardLogicalClusters},
		{Name: logicalClustersName, Patterns: patterns, VirtualWorkspace: logicalClusters},
		{Name: workspaceContentName, Patterns: patterns, VirtualWorkspace: workspaceContent},
	}, nil
}

// withTerminatorDomain keys the API domain by the terminator.
func withTerminatorDomain(ctx context.Context, match framework.PathMatch) context.Context {
	return dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(match.Value(terminatorWildcard)))
}

// resolveWildcardLogicalClusters accepts wildcard requests only.
func resolveWildcardLogicalClusters(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	if cluster := genericapirequest.ClusterFrom(ctx); cluster == nil || !cluster.Wildcard {
		return false, "", ctx
	}
	return true, match.PrefixToStrip(), withTerminatorDomain(ctx, match)
}

// resolveLogicalClusters accepts logicalclusters.core.kcp.io and discovery
// requests for a specific cluster.
func resolveLogicalClusters(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	if cluster := genericapirequest.ClusterFrom(ctx); cluster == nil || cluster.Wildcard {
		return false, "", ctx
	}
	resourceURL := "/" + match.Trail
	if !isLogicalClusterRequest(resourceURL) && !isDiscoveryRequest(resourceURL) {
		return false, "", ctx
	}
	return true, match.PrefixToStrip(), withTerminatorDomain(ctx, match)
}

// resolveWorkspaceContent accepts requests for a specific cluster
// except logicalclusters.core.kcp.io.
// The cluster segment is kept since the request is proxied, not consumed.
func resolveWorkspaceContent(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Wildcard {
		return false, "", ctx
	}
	if isLogicalClusterRequest("/" + match.Trail) {
		return false, "", ctx
	}
	prefixToStrip = strings.TrimSuffix(match.PrefixToStrip(), cluster.Name.Path().RequestPath())
	return true, prefixToStrip, withTerminatorDomain(ctx, match)
}

// resolveTerminatorWorkspaceType resolves the WorkspaceType referenced by a
// terminator. It returns:
//
//   - (wst, nil)  — the terminator encodes a WST reference and the WST was
//     found in the local or cache-server indexer; caller should evaluate
//     wst.Spec.TerminatorPermissions (Mode 1).
//   - (nil, nil)  — the terminator is a known system terminator: well-formed
//     but intentionally not backed by a WST. Caller should fall through to
//     owner impersonation (Mode 2).
//   - (nil, err)  — either the terminator is malformed (TypeFrom failed) or
//     it encodes a real (non-system) WST reference that neither indexer has.
//     Both cases must fail closed: granting Mode 2 to a terminator we cannot
//     validate would be a silent privilege escalation.
func resolveTerminatorWorkspaceType(
	terminator corev1alpha1.LogicalClusterTerminator,
	localWSTIndexer, cachedWSTIndexer cache.Indexer,
) (*tenancyv1alpha1.WorkspaceType, error) {
	return virtualshared.ResolveLifecycleWorkspaceType(
		"terminator", string(terminator),
		func() (logicalcluster.Name, string, error) { return termination.TypeFrom(terminator) },
		localWSTIndexer, cachedWSTIndexer,
	)
}

// URLFor returns the absolute path for the specified terminator.
func URLFor(terminatorName corev1alpha1.LogicalClusterTerminator) string {
	return path.Join("/services", terminatingworkspaces.VirtualWorkspaceName, string(terminatorName))
}

var resolver = requestinfo.NewFactory()

func isLogicalClusterRequest(path string) bool {
	info, err := resolver.NewRequestInfo(&http.Request{URL: &url.URL{Path: path}})
	if err != nil {
		return false
	}
	return info.IsResourceRequest && info.APIGroup == corev1alpha1.SchemeGroupVersion.Group && info.Resource == "logicalclusters"
}

type singleResourceAPIDefinitionSetProvider struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient kcpdynamic.ClusterInterface
	resource             *apisv1alpha1.APIResourceSchema
	exposeSubresources   bool
	storageProvider      func(ctx context.Context, clusterClient kcpdynamic.ClusterInterface, terminator corev1alpha1.LogicalClusterTerminator) (apiserver.RestProviderFunc, error)
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	restProvider, err := a.storageProvider(ctx, a.dynamicClusterClient, corev1alpha1.LogicalClusterTerminator(key))
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

func authorizerWithCache(ctx context.Context, cache delegated.Cache, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	// No impersonation is allowed through this virtual workspace. Any request attempting
	// to use the "impersonate" verb will be denied outright.
	if attr.GetVerb() == "impersonate" {
		return authorizer.DecisionDeny, "impersonation is not permitted on the terminatingworkspaces virtual workspace", nil
	}

	clusterName, name, err := termination.TypeFrom(corev1alpha1.LogicalClusterTerminator(dynamiccontext.APIDomainKeyFrom(ctx)))
	if err != nil {
		klog.FromContext(ctx).V(2).Info(err.Error())
		return authorizer.DecisionNoOpinion, "unable to determine terminator", fmt.Errorf("access not permitted")
	}

	authz, err := cache.Get(clusterName)
	if err != nil {
		return authorizer.DecisionNoOpinion, "error", err
	}

	SARAttributes := authorizer.AttributesRecord{
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            "terminate",
		Name:            name,
		Resource:        "workspacetypes",
		ResourceRequest: true,
	}

	return authz.Authorize(ctx, SARAttributes)
}

// isDiscoverRequest determines if a request is a discovery request or discovery
// caching request. Specifically, it allows:
// * /api
// * /api/{version}
// * /apis
// * /apis/{api-group}
// * /apis/{api-group}/{version}
// as long as they have no sub-paths.
func isDiscoveryRequest(path string) bool {
	info, err := resolver.NewRequestInfo(&http.Request{URL: &url.URL{Path: path}})
	if err != nil {
		return false
	}

	// eliminate any resource paths directly
	if info.IsResourceRequest {
		return false
	}

	// eliminate any non-resource paths which are not part
	// of discovery or discovery caching
	if info.Path == "/healthz" || info.Path == "/" {
		return false
	}

	return true
}
