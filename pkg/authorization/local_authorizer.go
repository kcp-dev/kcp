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

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	rbacv1listers "github.com/kcp-dev/client-go/listers/rbac/v1"

	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
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
	roleLister               rbacv1listers.RoleClusterLister
	roleBindingLister        rbacv1listers.RoleBindingClusterLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	clusterRoleLister        rbacv1listers.ClusterRoleClusterLister
}

func NewLocalAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &LocalAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
	}
	return a, a
}

func (a *LocalAuthorizer) RulesFor(user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	// TODO: wire context in RulesFor interface
	panic("implement me")
}

func (a *LocalAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		kaudit.AddAuditAnnotations(
			ctx,
			LocalAuditDecision, DecisionNoOpinion,
			LocalAuditReason, "empty cluster name",
		)
		return authorizer.DecisionNoOpinion, "", nil
	}

	scopedAuth := rbac.New(
		&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
			a.roleLister.Cluster(cluster.Name),
			a.roleLister.Cluster(genericcontrolplane.LocalAdminCluster),
		)},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Cluster(cluster.Name)},
		&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
			a.clusterRoleLister.Cluster(cluster.Name),
			a.clusterRoleLister.Cluster(genericcontrolplane.LocalAdminCluster),
		)},
		&rbac.ClusterRoleBindingLister{Lister: a.clusterRoleBindingLister.Cluster(cluster.Name)},
	)

	dec, reason, err := scopedAuth.Authorize(ctx, attr)

	kaudit.AddAuditAnnotations(
		ctx,
		LocalAuditDecision, DecisionString(dec),
		LocalAuditReason, fmt.Sprintf("cluster %q or bootstrap policy reason: %v", cluster.Name, reason),
	)

	return dec, reason, err
}
