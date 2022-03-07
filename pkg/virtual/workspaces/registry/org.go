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

package registry

import (
	"time"

	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"

	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
)

func CreateAndStartOrg(orgRBACClient rbacv1client.RbacV1Interface, orgClusteWorkspaceClient tenancyclient.ClusterWorkspaceInterface, orgRBACInformers rbacinformers.Interface, orgCRBInformer rbacinformers.ClusterRoleBindingInformer, orgClusterWorkspaceInformer workspaceinformer.ClusterWorkspaceInformer) Org {
	orgSubjectLocator := frameworkrbac.NewSubjectLocator(orgRBACInformers)
	orgReviewerProvider := workspaceauth.NewAuthorizerReviewerProvider(orgSubjectLocator)

	orgWorkspaceAuthorizationCache := workspaceauth.NewAuthorizationCache(
		orgClusterWorkspaceInformer.Lister(),
		orgClusterWorkspaceInformer.Informer(),
		orgReviewerProvider.ForVerb("get"),
		orgRBACInformers,
	)

	newOrg := Org{
		rbacClient:                orgRBACClient,
		crbInformer:               orgCRBInformer,
		crbLister:                 orgCRBInformer.Lister(),
		workspaceReviewerProvider: orgReviewerProvider,
		clusterWorkspaceClient:    orgClusteWorkspaceClient,
		clusterWorkspaceLister:    orgWorkspaceAuthorizationCache,
		stopCh:                    make(chan struct{}),
		authCache:                 orgWorkspaceAuthorizationCache,
	}

	newOrg.authCache.Run(1*time.Second, newOrg.stopCh)

	return newOrg
}

type Org struct {
	rbacClient             rbacv1client.RbacV1Interface
	crbInformer            rbacinformers.ClusterRoleBindingInformer
	crbLister              rbacv1listers.ClusterRoleBindingLister
	clusterWorkspaceClient tenancyclient.ClusterWorkspaceInterface

	// workspaceReviewerProvider allow getting a reviewer that checks
	// permissions for a given verb to workspaces
	workspaceReviewerProvider workspaceauth.ReviewerProvider
	// workspaceLister can enumerate workspace lists that enforce policy
	clusterWorkspaceLister workspaceauth.Lister
	// authCache is a cache of cluster workspaces and associated subjects for a given org.
	authCache *workspaceauth.AuthorizationCache
	// stopCh allows stopping the authCache for this org.
	stopCh chan struct{}
}

func (o Org) Ready() bool {
	return o.authCache.ReadyForAccess()
}

func (o Org) Stop() {
	o.stopCh <- struct{}{}
}
