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
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	BootstrapPolicyAuditPrefix   = "bootstrap.authorization.kcp.dev/"
	BootstrapPolicyAuditDecision = BootstrapPolicyAuditPrefix + "decision"
	BootstrapPolicyAuditReason   = BootstrapPolicyAuditPrefix + "reason"
)

type BootstrapPolicyAuthorizer struct {
	delegate *rbac.RBACAuthorizer
}

func NewBootstrapPolicyAuthorizer(informers kubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	filteredInformer := rbacwrapper.FilterInformers(genericcontrolplane.LocalAdminCluster, informers.Rbac().V1())

	a := &BootstrapPolicyAuthorizer{delegate: rbac.New(
		&rbac.RoleGetter{Lister: filteredInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: filteredInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: filteredInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: filteredInformer.ClusterRoleBindings().Lister()},
	)}

	return a, a
}

func (a *BootstrapPolicyAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	dec, reason, err := a.delegate.Authorize(ctx, attr)

	kaudit.AddAuditAnnotations(
		ctx,
		BootstrapPolicyAuditDecision, decisionString(dec),
		BootstrapPolicyAuditReason, fmt.Sprintf("bootstrap policy reason: %v", reason),
	)

	return dec, reason, err
}

func (a *BootstrapPolicyAuthorizer) RulesFor(user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	return a.delegate.RulesFor(user, namespace)
}
