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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// NewLifecycleGroupsAuthorizer returns an authorizer that allows
// requests for the lifecycle groups if the logical cluster is in
// a bypass-allowed phase.
// The virtual workspace already authorized the request against the
// WorkspaceType's initializer/terminator permissions and impersonates
// the respective lifecycle group.
func NewLifecycleGroupsAuthorizer(getLogicalCluster LogicalClusterGetter) authorizer.Authorizer {
	return &lifecycleGroupsAuthorizer{
		getLogicalCluster: getLogicalCluster,
	}
}

type lifecycleGroupsAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
}

func (a *lifecycleGroupsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR asks what the policy would decide, not what a pre-authorized proxy may do
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	if attr.GetUser() == nil || !hasLifecycleGroup(attr.GetUser().GetGroups()) {
		return authorizer.DecisionNoOpinion, "no lifecycle group", nil
	}

	logicalCluster, err := a.getLogicalCluster(cluster.Name)
	if err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionNoOpinion, "LogicalCluster not found", nil
		}
		return authorizer.DecisionNoOpinion, "error getting LogicalCluster", err
	}

	// Validate that the request carries a synthetic group for a valid
	// registered intiializer/terminator.
	switch logicalCluster.Status.Phase {
	case corev1alpha1.LogicalClusterPhaseInitializing:
		if isRegisteredInitializer(attr.GetUser().GetGroups(), logicalCluster) {
			return authorizer.DecisionAllow, "initializing lifecycle VW pre-authorized access", nil
		}
	case corev1alpha1.LogicalClusterPhaseTerminating, corev1alpha1.LogicalClusterPhaseDeleting:
		if isRegisteredTerminator(attr.GetUser().GetGroups(), logicalCluster) {
			return authorizer.DecisionAllow, "terminating lifecycle VW pre-authorized access", nil
		}
	}

	return authorizer.DecisionNoOpinion, fmt.Sprintf("no registered lifecycle group in phase %q", logicalCluster.Status.Phase), nil
}

// ConditionsAwareAuthorize. This authorizer is not conditions-aware and therefore returns unconditional Authorize decision.
func (a *lifecycleGroupsAuthorizer) ConditionsAwareAuthorize(ctx context.Context, attrs authorizer.Attributes) authorizer.ConditionsAwareDecision {
	return authorizer.ConditionsAwareDecisionFromParts(a.Authorize(ctx, attrs))
}

// EvaluateConditions fails closed because this authorizer is not conditions-aware.
func (a *lifecycleGroupsAuthorizer) EvaluateConditions(_ context.Context, _ authorizer.ConditionsAwareDecision, _ authorizer.ConditionsData) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "", authorizer.ErrorConditionEvaluationNotSupported
}
