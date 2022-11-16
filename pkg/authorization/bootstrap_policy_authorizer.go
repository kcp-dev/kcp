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

	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

const (
	BootstrapPolicyAuditPrefix   = "bootstrap.authorization.kcp.dev/"
	BootstrapPolicyAuditDecision = BootstrapPolicyAuditPrefix + "decision"
	BootstrapPolicyAuditReason   = BootstrapPolicyAuditPrefix + "reason"
)

type BootstrapPolicyAuthorizer struct {
	delegate *rbac.RBACAuthorizer
}

func NewBootstrapPolicyAuthorizer(informers kcpkubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &BootstrapPolicyAuthorizer{delegate: rbac.New(
		&rbac.RoleGetter{Lister: informers.Rbac().V1().Roles().Lister().Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.RoleBindingLister{Lister: informers.Rbac().V1().RoleBindings().Lister().Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.ClusterRoleGetter{Lister: informers.Rbac().V1().ClusterRoles().Lister().Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.ClusterRoleBindingLister{Lister: informers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(genericcontrolplane.LocalAdminCluster)},
	)}

	return a, a
}

func (a *BootstrapPolicyAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	dec, reason, err := a.delegate.Authorize(ctx, attr)

	kaudit.AddAuditAnnotations(
		ctx,
		BootstrapPolicyAuditDecision, DecisionString(dec),
		BootstrapPolicyAuditReason, fmt.Sprintf("bootstrap policy reason: %v", reason),
	)

	return dec, reason, err
}

func (a *BootstrapPolicyAuthorizer) RulesFor(ctx context.Context, user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	return a.delegate.RulesFor(ctx, user, namespace)
}
