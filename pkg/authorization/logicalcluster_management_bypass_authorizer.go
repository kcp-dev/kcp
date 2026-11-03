/*
Copyright 2026 The kcp Authors.

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

	"k8s.io/apiserver/pkg/authorization/authorizer"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// logicalClusterManagementGroups are the identities that operate on logical clusters themselves,
// in every phase and before any content of the logical cluster exists.
var logicalClusterManagementGroups = []string{
	bootstrap.SystemLogicalClusterAdmin,
	bootstrap.SystemExternalLogicalClusterAdmin,
}

// NewLogicalClusterManagementBypassAuthorizer admits requests from
// logical cluster admin identities according to the bootstrap policy.
func NewLogicalClusterManagementBypassAuthorizer(bootstrapPolicy authorizer.Authorizer) authorizer.Authorizer {
	return &logicalClusterManagementBypassAuthorizer{
		bootstrapPolicy: bootstrapPolicy,
	}
}

type logicalClusterManagementBypassAuthorizer struct {
	bootstrapPolicy authorizer.Authorizer
}

func (a *logicalClusterManagementBypassAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR reviews another identity, the caller's group must not admit the payload request
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	effGroups := rbacregistryvalidation.EffectiveGroups(ctx, attr.GetUser())
	found := false
	for _, group := range logicalClusterManagementGroups {
		if effGroups.Has(group) {
			found = true
			break
		}
	}
	if !found {
		return authorizer.DecisionNoOpinion, "not a logical cluster management identity", nil
	}

	dec, reason, err := a.bootstrapPolicy.Authorize(ctx, attr)
	if err != nil {
		return authorizer.DecisionNoOpinion, "error checking bootstrap policy", err
	}
	if dec != authorizer.DecisionAllow {
		return authorizer.DecisionNoOpinion, "not covered by the bootstrap policy", nil
	}

	return authorizer.DecisionAllow, "logical cluster management: " + reason, nil
}

// ConditionsAwareAuthorize. This authorizer is not conditions-aware and therefore returns unconditional Authorize decision.
func (a *logicalClusterManagementBypassAuthorizer) ConditionsAwareAuthorize(ctx context.Context, attrs authorizer.Attributes) authorizer.ConditionsAwareDecision {
	return authorizer.ConditionsAwareDecisionFromParts(a.Authorize(ctx, attrs))
}

// EvaluateConditions fails closed because this authorizer is not conditions-aware.
func (a *logicalClusterManagementBypassAuthorizer) EvaluateConditions(_ context.Context, _ authorizer.ConditionsAwareDecision, _ authorizer.ConditionsData) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "", authorizer.ErrorConditionEvaluationNotSupported
}
