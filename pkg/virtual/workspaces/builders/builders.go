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

package builders

import (
	"context"
	"strings"
	"time"

	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
	rbacauthorizer "k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	builders "github.com/kcp-dev/kcp/pkg/virtual/generic/builders"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	virtualworkspacesregistry "github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

const WorkspacesVirtualWorkspaceName string = "workspaces"
const DefaultRootPathPrefix string = "/services/applications"

func WorkspacesVirtualWorkspaceBuilder(rootPathPrefix string, workspaces workspaceinformer.WorkspaceInformer, workspaceClient workspaceclient.WorkspaceInterface, kubeClient kubernetes.Interface, rbacInformers rbacinformers.Interface, subjectLocator rbacauthorizer.SubjectLocator, ruleResolver rbacregistryvalidation.AuthorizationRuleResolver) builders.VirtualWorkspaceBuilder {
	crbInformer := rbacInformers.ClusterRoleBindings()
	_ = virtualworkspacesregistry.AddNameIndexers(crbInformer)

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}
	var workspaceAuthorizationCache *workspaceauth.AuthorizationCache
	return builders.VirtualWorkspaceBuilder{
		Name: WorkspacesVirtualWorkspaceName,
		Ready: func() bool {
			return workspaceAuthorizationCache != nil && workspaceAuthorizationCache.ReadyForAccess()
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
		GroupAPIServerBuilders: []builders.APIGroupAPIServerBuilder{
			{
				GroupVersion: tenancyv1alpha1.SchemeGroupVersion,
				AddToScheme:  tenancyv1alpha1.AddToScheme,
				Initialize: func(mainConfig genericapiserver.CompletedConfig) (map[string]builders.RestStorageBuilder, error) {
					reviewerProvider := workspaceauth.NewAuthorizerReviewerProvider(subjectLocator)
					workspaceAuthorizationCache = workspaceauth.NewAuthorizationCache(
						workspaces.Lister(),
						workspaces.Informer(),
						reviewerProvider.ForVerb("get"),
						rbacInformers,
					)

					workspaceCache := workspacecache.NewWorkspaceCache(
						workspaces.Informer(),
						workspaceClient,
						"")

					if err := mainConfig.AddPostStartHook("workspaces.kcp.dev-workspacecache", func(context genericapiserver.PostStartHookContext) error {
						go workspaceCache.Run(context.StopCh)
						return nil
					}); err != nil {
						return nil, err
					}
					if err := mainConfig.AddPostStartHook("workspaces.kcp.dev-workspaceauthorizationcache", func(context genericapiserver.PostStartHookContext) error {
						period := 1 * time.Second
						workspaceAuthorizationCache.Run(period)
						return nil
					}); err != nil {
						return nil, err
					}

					return map[string]builders.RestStorageBuilder{
						"workspaces": func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (rest.Storage, error) {
							return virtualworkspacesregistry.NewREST(workspaceClient, kubeClient.RbacV1(), crbInformer, reviewerProvider, workspaceAuthorizationCache), nil
						},
					}, nil
				},
			},
		},
	}
}
