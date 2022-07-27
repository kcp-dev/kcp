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

	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	clientrest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
	tenancywrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/tenancy"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

func BuildVirtualWorkspace(cfg *clientrest.Config, rootPathPrefix string, wildcardsClusterWorkspaces workspaceinformer.ClusterWorkspaceInformer, wildcardsRbacInformers rbacinformers.Interface, kubeClusterClient kubernetes.Interface, kcpClusterClient kcpclient.ClusterInterface) framework.VirtualWorkspace {
	crbInformer := wildcardsRbacInformers.ClusterRoleBindings()
	_ = registry.AddNameIndexers(crbInformer)

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
				segments := strings.SplitN(path, "/", 3)
				if len(segments) < 2 {
					return
				}
				org, scope := segments[0], segments[1]
				if !registry.ScopeSet.Has(scope) {
					return
				}

				return true, rootPathPrefix + strings.Join(segments[:2], "/"),
					context.WithValue(
						context.WithValue(requestContext, registry.WorkspacesScopeKey, scope),
						registry.WorkspacesOrgKey, logicalcluster.New(org),
					)
			}
			return
		}),
		Authorizer: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
			klog.Error("the authorizer for the 'workspaces' virtual workspace is not implemented !")
			return authorizer.DecisionAllow, "", nil
		}),
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
							Resource(tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaces"), "content").
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

					workspacesRest := registry.NewREST(cfg, kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1(), kubeClusterClient, kcpClusterClient, globalClusterWorkspaceCache, crbInformer, orgListener.FilteredClusterWorkspaces)
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
