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

package authorization

import (
	"context"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	kcpclientgoinformers "k8s.io/client-go/kcp/informers"
	kcprbaclister "k8s.io/client-go/kcp/listers/rbac/v1"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

type LocalAuthorizer struct {
	roleLister               *kcprbaclister.RoleClusterLister
	roleBindingLister        *kcprbaclister.RoleBindingClusterLister
	clusterRoleBindingLister *kcprbaclister.ClusterRoleBindingClusterLister
	clusterRoleLister        *kcprbaclister.ClusterRoleClusterLister
}

func NewLocalAuthorizer(versionedInformers *kcpclientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &LocalAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister().(*kcprbaclister.RoleClusterLister),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister().(*kcprbaclister.RoleBindingClusterLister),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister().(*kcprbaclister.ClusterRoleClusterLister),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister().(*kcprbaclister.ClusterRoleBindingClusterLister),
	}
	return a, a
}

func (a *LocalAuthorizer) RulesFor(user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	// TODO: wire context in RulesFor interface
	panic("implement me")
}

func (a *LocalAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "", nil
	}

	reqScope := cluster.Name
	scopedAuth := rbac.New(
		&rbac.RoleGetter{Lister: rbacwrapper.MergedRoleLister(a.roleLister.Cluster(reqScope), a.roleLister.Cluster(genericcontrolplane.LocalAdminCluster))},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Cluster(reqScope)},
		&rbac.ClusterRoleGetter{Lister: rbacwrapper.MergedClusterRoleLister(a.clusterRoleLister.Cluster(reqScope), a.clusterRoleLister.Cluster(genericcontrolplane.LocalAdminCluster))},
		&rbac.ClusterRoleBindingLister{Lister: a.clusterRoleBindingLister.Cluster(reqScope)},
	)

	return scopedAuth.Authorize(ctx, attr)
}
