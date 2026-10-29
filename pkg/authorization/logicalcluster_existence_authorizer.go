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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// LogicalClusterGetter returns the LogicalCluster object backing a logical cluster.
type LogicalClusterGetter func(logicalCluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

// NewLogicalClusterExistenceAuthorizer returns an authorizer that denies requests against logical clusters that do not exist.
func NewLogicalClusterExistenceAuthorizer(getLogicalCluster LogicalClusterGetter) authorizer.Authorizer {
	return &logicalClusterExistenceAuthorizer{
		getLogicalCluster: getLogicalCluster,
	}
}

type logicalClusterExistenceAuthorizer struct {
	getLogicalCluster LogicalClusterGetter
}

func (a *logicalClusterExistenceAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR asks what the policy would decide, not whether the workspace exists
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	// empty and system clusters have no meaning in the context of workspace content and are
	// not backed by a LogicalCluster object - same skip as the workspace content authorizer.
	if cluster == nil || cluster.Name.Empty() || strings.HasPrefix(cluster.Name.String(), "system:") {
		return authorizer.DecisionNoOpinion, "empty or system workspace", nil
	}

	// Skip LC existence check for LC admin, which is used for creating LCs in the first place.
	if rbacregistryvalidation.EffectiveGroups(ctx, attr.GetUser()).Has(bootstrap.SystemLogicalClusterAdmin) {
		return authorizer.DecisionNoOpinion, "logical cluster admin access", nil
	}

	if _, err := a.getLogicalCluster(cluster.Name); err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, "LogicalCluster not found", nil
		}
		return authorizer.DecisionNoOpinion, "error getting LogicalCluster", err
	}

	return authorizer.DecisionNoOpinion, "LogicalCluster exists", nil
}

// ConditionsAwareAuthorize. This authorizer is not conditions-aware and therefore returns unconditional Authorize decision.
func (a *logicalClusterExistenceAuthorizer) ConditionsAwareAuthorize(ctx context.Context, attrs authorizer.Attributes) authorizer.ConditionsAwareDecision {
	return authorizer.ConditionsAwareDecisionFromParts(a.Authorize(ctx, attrs))
}

// EvaluateConditions fails closed because this authorizer is not conditions-aware.
func (a *logicalClusterExistenceAuthorizer) EvaluateConditions(_ context.Context, _ authorizer.ConditionsAwareDecision, _ authorizer.ConditionsData) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "", authorizer.ErrorConditionEvaluationNotSupported
}
