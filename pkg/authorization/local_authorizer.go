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
	"fmt"

	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	kubernetesinformers "k8s.io/client-go/informers"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	LocalAuditPrefix   = "local.authorization.kcp.dev/"
	LocalAuditDecision = LocalAuditPrefix + "decision"
	LocalAuditReason   = LocalAuditPrefix + "reason"
)

type LocalAuthorizer struct {
	roleLister               rbaclisters.RoleLister
	roleBindingLister        rbaclisters.RoleBindingLister
	clusterRoleBindingLister rbaclisters.ClusterRoleBindingLister
	clusterRoleLister        rbaclisters.ClusterRoleLister

	// TODO: this will go away when scoping lands. Then we only have those 4 listers above.
	versionedInformers kubernetesinformers.SharedInformerFactory
}

func NewLocalAuthorizer(versionedInformers kubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
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
		kaudit.AddAuditAnnotations(
			ctx,
			LocalAuditDecision, DecisionNoOpinion,
			LocalAuditReason, fmt.Sprintf("error getting cluster from request: %v", err),
		)
		return authorizer.DecisionNoOpinion, "", err
	}
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "", nil
	}

	reqScope := cluster.Name
	filteredInformer := rbacwrapper.FilterInformers(reqScope, a.versionedInformers.Rbac().V1())
	bootstrapInformer := rbacwrapper.FilterInformers(genericcontrolplane.LocalAdminCluster, a.versionedInformers.Rbac().V1())

	mergedClusterRoleInformer := rbacwrapper.MergedClusterRoleInformer(filteredInformer.ClusterRoles(), bootstrapInformer.ClusterRoles())
	mergedRoleInformer := rbacwrapper.MergedRoleInformer(filteredInformer.Roles(), bootstrapInformer.Roles())

	scopedAuth := rbac.New(
		&rbac.RoleGetter{Lister: mergedRoleInformer.Lister()},
		&rbac.RoleBindingLister{Lister: filteredInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: mergedClusterRoleInformer.Lister()},
		&rbac.ClusterRoleBindingLister{Lister: filteredInformer.ClusterRoleBindings().Lister()},
	)

	dec, reason, err := scopedAuth.Authorize(ctx, attr)

	kaudit.AddAuditAnnotations(
		ctx,
		LocalAuditDecision, decisionString(dec),
		LocalAuditReason, fmt.Sprintf("cluster %q or bootstrap policy reason: %v", cluster.Name, reason),
	)

	return dec, reason, err
}
