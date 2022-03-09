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

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
	tenancywrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/tenancy"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	virtualworkspacesregistry "github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

const WorkspacesVirtualWorkspaceName string = "workspaces"
const DefaultRootPathPrefix string = "/services/workspaces"

func BuildVirtualWorkspace(rootPathPrefix string, wildcardsClusterWorkspaces workspaceinformer.ClusterWorkspaceInformer, wildcardsRbacInformers rbacinformers.Interface, rootKcpClient kcpclient.Interface, rootKubeClient kubernetes.Interface, kcpClusterInterface kcpclient.ClusterInterface, kubeClusterInterface kubernetes.ClusterInterface) framework.VirtualWorkspace {
	crbInformer := wildcardsRbacInformers.ClusterRoleBindings()
	_ = virtualworkspacesregistry.AddNameIndexers(crbInformer)

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	var rootWorkspaceAuthorizationCache *workspaceauth.AuthorizationCache
	var globalClusterWorkspaceCache *workspacecache.ClusterWorkspaceCache
	var orgListener *orgListener

	return &fixedgvs.FixedGroupVersionsVirtualWorkspace{
		Name: WorkspacesVirtualWorkspaceName,
		Ready: func() error {
			if globalClusterWorkspaceCache == nil || !globalClusterWorkspaceCache.HasSynced() {
				return errors.New("ClusterWorkspaceCache is not ready for access")
			}

			if rootWorkspaceAuthorizationCache == nil || !rootWorkspaceAuthorizationCache.ReadyForAccess() {
				return errors.New("WorkspaceAuthorizationCache is not ready for access")
			}

			if orgListener == nil || !orgListener.Ready() {
				return errors.New("Organization listener is not ready for access")
			}
			return nil
		},
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext
			if path := urlPath; strings.HasPrefix(path, rootPathPrefix) {
				path = strings.TrimPrefix(path, rootPathPrefix)
				segments := strings.SplitN(path, "/", 3)
				if len(segments) < 2 {
					return
				}
				org, scope := segments[0], segments[1]
				if !virtualworkspacesregistry.ScopeSet.Has(scope) {
					return
				}

				// Do not allow the personal scope when accessing orgs as workspaces in the root logical cluster
				if scope == virtualworkspacesregistry.PersonalScope && org == helper.RootCluster {
					return
				}

				return true, rootPathPrefix + strings.Join(segments[:2], "/"),
					context.WithValue(
						context.WithValue(requestContext, virtualworkspacesregistry.WorkspacesScopeKey, scope),
						virtualworkspacesregistry.WorkspacesOrgKey, org,
					)
			}
			return
		},
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion:       tenancyv1beta1.SchemeGroupVersion,
				AddToScheme:        tenancyv1beta1.AddToScheme,
				OpenAPIDefinitions: kcpopenapi.GetOpenAPIDefinitions,
				BootstrapRestResources: func(mainConfig genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {

					rootTenancyClient := rootKcpClient.TenancyV1alpha1()
					rootRBACClient := rootKubeClient.RbacV1()

					globalClusterWorkspaceCache = workspacecache.NewClusterWorkspaceCache(
						wildcardsClusterWorkspaces.Informer(),
						kcpClusterInterface,
						"")

					rootRBACInformers := rbacwrapper.FilterInformers(helper.RootCluster, wildcardsRbacInformers)
					rootSubjectLocator := frameworkrbac.NewSubjectLocator(rootRBACInformers)
					rootReviewer := workspaceauth.NewReviewer(rootSubjectLocator)

					rootClusterWorkspaceInformer := tenancywrapper.FilterClusterWorkspaceInformer(helper.RootCluster, wildcardsClusterWorkspaces)

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

					rootOrg := virtualworkspacesregistry.RootOrg(rootRBACClient, rootRBACInformers.ClusterRoleBindings(), rootReviewer, rootTenancyClient.ClusterWorkspaces(), rootWorkspaceAuthorizationCache)

					orgListener = NewOrgListener(globalClusterWorkspaceCache, rootOrg, func(orgClusterName string) *virtualworkspacesregistry.Org {
						return virtualworkspacesregistry.CreateAndStartOrg(
							kubeClusterInterface.Cluster(orgClusterName).RbacV1(),
							kcpClusterInterface.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces(),
							rbacwrapper.FilterInformers(orgClusterName, wildcardsRbacInformers),
							rbacwrapper.FilterClusterRoleBindingInformer(orgClusterName, crbInformer),
							tenancywrapper.FilterClusterWorkspaceInformer(orgClusterName, wildcardsClusterWorkspaces))
					})

					if err := mainConfig.AddPostStartHook("clusterworkspaces.kcp.dev-workspacecache", func(context genericapiserver.PostStartHookContext) error {
						go globalClusterWorkspaceCache.Run(context.StopCh)
						return nil
					}); err != nil {
						return nil, err
					}
					if err := mainConfig.AddPostStartHook("clusterworkspaces.kcp.dev-workspaceauthorizationcache", func(context genericapiserver.PostStartHookContext) error {
						for _, informer := range []cache.SharedIndexInformer{
							wildcardsClusterWorkspaces.Informer(),
							wildcardsRbacInformers.ClusterRoleBindings().Informer(),
							wildcardsRbacInformers.RoleBindings().Informer(),
							wildcardsRbacInformers.ClusterRoles().Informer(),
							wildcardsRbacInformers.Roles().Informer(),
						} {
							if !cache.WaitForNamedCacheSync("workspaceauthorizationcache", context.StopCh, informer.HasSynced) {
								return errors.New("informer not synced")
							}
						}
						rootWorkspaceAuthorizationCache.Run(1*time.Second, context.StopCh)
						go func() {
							_ = wait.PollImmediateUntil(100*time.Millisecond, func() (done bool, err error) {
								return rootWorkspaceAuthorizationCache.ReadyForAccess(), nil
							}, context.StopCh)
							orgListener.Initialize(rootWorkspaceAuthorizationCache)
						}()
						return nil
					}); err != nil {
						return nil, err
					}

					workspacesRest, kubeconfigSubresourceRest := virtualworkspacesregistry.NewREST(rootKcpClient.TenancyV1alpha1(), rootKubeClient, globalClusterWorkspaceCache, crbInformer, orgListener.GetOrg, rootReviewer)
					return map[string]fixedgvs.RestStorageBuilder{
						"workspaces": func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (rest.Storage, error) {
							return workspacesRest, nil
						},
						"workspaces/kubeconfig": func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (rest.Storage, error) {
							return kubeconfigSubresourceRest, nil
						},
					}, nil
				},
			},
		},
	}
}
