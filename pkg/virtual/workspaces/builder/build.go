/*
Copyright 2021 The KCP Authors.

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
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/util/sets"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
	clientrest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
	"github.com/kcp-dev/kcp/pkg/softimpersonation"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
	tenancywrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/tenancy"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

func BuildVirtualWorkspace(cfg *clientrest.Config, rootPathPrefix string, wildcardsClusterWorkspaces tenancyinformers.ClusterWorkspaceInformer, wildcardsRbacInformers rbacinformers.Interface, kubeClusterClient kubernetesclient.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface) framework.VirtualWorkspace {
	crbInformer := wildcardsRbacInformers.ClusterRoleBindings()

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	var rootWorkspaceAuthorizationCache *workspaceauth.AuthorizationCache
	var globalClusterWorkspaceCache *workspacecache.ClusterWorkspaceCache

	return &fixedgvs.FixedGroupVersionsVirtualWorkspace{
		ReadyChecker: framework.ReadyFunc(func() error {
			if globalClusterWorkspaceCache == nil || !globalClusterWorkspaceCache.HasSynced() {
				return errors.New("ClusterWorkspaceCache is not ready for access")
			}

			if rootWorkspaceAuthorizationCache == nil || !rootWorkspaceAuthorizationCache.ReadyForAccess() {
				return errors.New("WorkspaceAuthorizationCache is not ready for access")
			}

			return nil
		}),
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext
			if path := urlPath; strings.HasPrefix(path, rootPathPrefix) {
				path = strings.TrimPrefix(path, rootPathPrefix)
				segments := strings.SplitN(path, "/", 2)
				if len(segments) < 2 {
					return
				}
				org := segments[0]

				return true, rootPathPrefix + strings.Join(segments[:1], "/"),
					context.WithValue(
						requestContext,
						registry.WorkspacesOrgKey, logicalcluster.New(org),
					)
			}
			return
		}),
		Authorizer: authorizer.AuthorizerFunc(newAuthorizer(cfg)),
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion:       tenancyv1beta1.SchemeGroupVersion,
				AddToScheme:        tenancyv1beta1.AddToScheme,
				OpenAPIDefinitions: kcpopenapi.GetOpenAPIDefinitions,
				BootstrapRestResources: func(mainConfig genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
					rootRBACInformers := rbacwrapper.FilterInformers(tenancyv1alpha1.RootCluster, wildcardsRbacInformers)
					rootSubjectLocator := frameworkrbac.NewSubjectLocator(rootRBACInformers)
					rootReviewer := workspaceauth.NewReviewer(rootSubjectLocator)
					rootClusterWorkspaceInformer := tenancywrapper.FilterClusterWorkspaceInformer(tenancyv1alpha1.RootCluster, wildcardsClusterWorkspaces)

					globalClusterWorkspaceCache = workspacecache.NewClusterWorkspaceCache(wildcardsClusterWorkspaces.Informer(), kcpClusterClient)

					rootWorkspaceAuthorizationCache = workspaceauth.NewAuthorizationCache(
						rootClusterWorkspaceInformer.Lister(),
						rootClusterWorkspaceInformer.Informer(),
						rootReviewer,
						*workspaceauth.NewAttributesBuilder().
							Verb("access").
							Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces"), "content").
							AttributesRecord,
						rootRBACInformers,
					)

					orgListener := NewOrgListener(wildcardsClusterWorkspaces, func(orgClusterName logicalcluster.Name, initialWatchers []workspaceauth.CacheWatcher) registry.FilteredClusterWorkspaces {
						return CreateAndStartOrg(
							rbacwrapper.FilterInformers(orgClusterName, wildcardsRbacInformers),
							tenancywrapper.FilterClusterWorkspaceInformer(orgClusterName, wildcardsClusterWorkspaces),
							initialWatchers)
					})

					if err := mainConfig.AddPostStartHook("clusterworkspaces.kcp.dev-workspaceauthorizationcache", func(context genericapiserver.PostStartHookContext) error {
						for _, informer := range []cache.SharedIndexInformer{
							wildcardsClusterWorkspaces.Informer(),
							wildcardsRbacInformers.ClusterRoleBindings().Informer(),
							wildcardsRbacInformers.RoleBindings().Informer(),
							wildcardsRbacInformers.ClusterRoles().Informer(),
							wildcardsRbacInformers.Roles().Informer(),
						} {
							if !cache.WaitForNamedCacheSync("workspaceauthorizationcache", context.StopCh, informer.HasSynced) {
								klog.Errorf("informer not synced")
								return nil
							}
						}
						rootWorkspaceAuthorizationCache.Run(1*time.Second, context.StopCh)
						return nil
					}); err != nil {
						return nil, err
					}

					workspacesRest := registry.NewREST(kubeClusterClient, kcpClusterClient, globalClusterWorkspaceCache, crbInformer, orgListener.FilteredClusterWorkspaces)
					return map[string]fixedgvs.RestStorageBuilder{
						"workspaces": func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (rest.Storage, error) {
							return workspacesRest, nil
						},
					}, nil
				},
			},
		},
	}
}

func newAuthorizer(cfg *clientrest.Config) func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	return func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		if sets.NewString(a.GetUser().GetGroups()...).Has(kuser.SystemPrivilegedGroup) {
			return authorizer.DecisionAllow, "", nil
		}

		if a.GetAPIGroup() != tenancy.GroupName || a.GetResource() != "workspaces" {
			return authorizer.DecisionNoOpinion, "", nil
		}

		// We need to softly impersonate the name of the user here, because the user Home workspace
		// might be created on-the-fly when receiving the SAR call.
		// And this automatically creation of the Home workspace needs to be done with the right user.
		//
		// We call this "soft" impersonation in the sense that the whole user JSON is added as an
		// additional request header, that will be explicitly read by the Home Workspace handler,
		// instead of changing the real user before authorization as for "hard" impersonation.
		impersonatedConfig, err := softimpersonation.WithSoftImpersonatedConfig(cfg, a.GetUser())
		if err != nil {
			klog.Errorf("failed to create impersonated kube cluster client: %v", err)
			return authorizer.DecisionNoOpinion, "", nil
		}
		softlyImpersonatedSARClusterClient, err := kubernetesclient.NewClusterForConfig(impersonatedConfig)
		if err != nil {
			klog.Errorf("failed to create impersonated kube cluster client: %v", err)
			return authorizer.DecisionNoOpinion, "", nil
		}

		// check for <verb> permission on the ClusterWorkspace workspace subresource for the <resourceName>
		clusterName := ctx.Value(registry.WorkspacesOrgKey).(logicalcluster.Name)
		authz, err := delegated.NewDelegatedAuthorizer(clusterName, softlyImpersonatedSARClusterClient)
		if err != nil {
			klog.Errorf("failed to get delegated authorizer for logical cluster %s", a.GetUser().GetName(), clusterName)
			return authorizer.DecisionNoOpinion, "", nil
		}
		workspaceAttr := authorizer.AttributesRecord{
			User:            a.GetUser(),
			Verb:            a.GetVerb(),
			APIGroup:        tenancyv1beta1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1beta1.SchemeGroupVersion.Version,
			Resource:        "workspaces",
			Name:            a.GetName(),
			ResourceRequest: true,
		}
		decision, reason, err := authz.Authorize(ctx, workspaceAttr)
		if err != nil {
			klog.Errorf("failed to authorize user %q to %q workspaces name %q in %s", a.GetUser().GetName(), a.GetVerb(), a.GetName(), clusterName)
			return authorizer.DecisionNoOpinion, "", nil
		}

		return decision, reason, nil
	}
}
