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

	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
	rbacauthorizer "k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	kcpopenapi "github.com/kcp-dev/kcp/pkg/openapi"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	virtualworkspacesregistry "github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

const WorkspacesVirtualWorkspaceName string = "workspaces"
const DefaultRootPathPrefix string = "/services/applications"

func BuildVirtualWorkspace(rootPathPrefix string, clusterWorkspaces workspaceinformer.ClusterWorkspaceInformer, kcpClient kcpclient.Interface, kubeClient kubernetes.Interface, rbacInformers rbacinformers.Interface, subjectLocator rbacauthorizer.SubjectLocator, ruleResolver rbacregistryvalidation.AuthorizationRuleResolver) framework.VirtualWorkspace {
	crbInformer := rbacInformers.ClusterRoleBindings()
	_ = virtualworkspacesregistry.AddNameIndexers(crbInformer)

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	var workspaceAuthorizationCache *workspaceauth.AuthorizationCache
	return &fixedgvs.FixedGroupVersionsVirtualWorkspace{
		Name: WorkspacesVirtualWorkspaceName,
		Ready: func() error {
			if workspaceAuthorizationCache == nil || !workspaceAuthorizationCache.ReadyForAccess() {
				return errors.New("WorkspaceAuthorizationCache is not ready for access")
			}
			return nil
		},
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext
			if path := urlPath; strings.HasPrefix(path, rootPathPrefix) {
				path = strings.TrimPrefix(path, rootPathPrefix)
				i := strings.Index(path, "/")
				if i == -1 {
					return
				}
				workspacesScope := path[:i]
				if !virtualworkspacesregistry.ScopeSets.Has(workspacesScope) {
					return
				}

				return true, rootPathPrefix + workspacesScope, context.WithValue(requestContext, virtualworkspacesregistry.WorkspacesScopeKey, workspacesScope)
			}
			return
		},
		GroupVersionAPISets: []fixedgvs.GroupVersionAPISet{
			{
				GroupVersion:       tenancyv1beta1.SchemeGroupVersion,
				AddToScheme:        tenancyv1beta1.AddToScheme,
				OpenAPIDefinitions: kcpopenapi.GetOpenAPIDefinitions,
				BootstrapRestResources: func(mainConfig genericapiserver.CompletedConfig) (map[string]fixedgvs.RestStorageBuilder, error) {
					reviewerProvider := workspaceauth.NewAuthorizerReviewerProvider(subjectLocator)
					workspaceAuthorizationCache = workspaceauth.NewAuthorizationCache(
						clusterWorkspaces.Lister(),
						clusterWorkspaces.Informer(),
						reviewerProvider.ForVerb("get"),
						rbacInformers,
					)

					workspaceClient := kcpClient.TenancyV1alpha1().ClusterWorkspaces()
					clusterWorkspaceCache := workspacecache.NewClusterWorkspaceCache(
						clusterWorkspaces.Informer(),
						workspaceClient,
						"")

					if err := mainConfig.AddPostStartHook("clusterworkspaces.kcp.dev-workspacecache", func(context genericapiserver.PostStartHookContext) error {
						go clusterWorkspaceCache.Run(context.StopCh)
						return nil
					}); err != nil {
						return nil, err
					}
					if err := mainConfig.AddPostStartHook("clusterworkspaces.kcp.dev-workspaceauthorizationcache", func(context genericapiserver.PostStartHookContext) error {
						period := 1 * time.Second
						workspaceAuthorizationCache.Run(period)
						return nil
					}); err != nil {
						return nil, err
					}

					workspacesRest, kubeconfigSubresourceRest := virtualworkspacesregistry.NewREST(kcpClient.TenancyV1alpha1(), kubeClient, crbInformer, reviewerProvider, workspaceAuthorizationCache)
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
