/*
Copyright The KCP Authors.

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
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
)

type LocalAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister

	// TODO: this will go away when scoping lands. Then we only have those 4 listers above.
	versionedInformers clientgoinformers.SharedInformerFactory
}

func NewLocalAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &LocalAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),

		versionedInformers: versionedInformers,
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
	if cluster == nil || cluster.Name == "" {
		return authorizer.DecisionNoOpinion, "", nil
	}

	reqScope := cluster.Name
	filteredInformer := frameworkrbac.FilterPerCluster(reqScope, a.versionedInformers.Rbac().V1())

	scopedAuth := rbac.New(
		&rbac.RoleGetter{Lister: filteredInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: filteredInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: filteredInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: filteredInformer.ClusterRoleBindings().Lister()},
	)

	return scopedAuth.Authorize(ctx, attr)
}
