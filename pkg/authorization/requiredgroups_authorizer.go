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
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	// RequiredGroupsAnnotationKey is a comma-separated list (OR'ed) of semicolon separated
	// groups (AND'ed) that a user must be a member of to be able to access the workspace.
	RequiredGroupsAnnotationKey = "authorization.kcp.io/required-groups"
)

// NewRequiredGroupsAuthorizer returns an authorizer that a set of groups stored
// on the LogicalCluster object. Service account by-pass this.
func NewRequiredGroupsAuthorizer(local, global corev1alpha1listers.LogicalClusterClusterLister) func(delegate authorizer.Authorizer) authorizer.Authorizer {
	return func(delegate authorizer.Authorizer) authorizer.Authorizer {
		return &requiredGroupsAuthorizer{
			getLogicalCluster: func(logicalCluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
				obj, err := local.Cluster(logicalCluster).Get(corev1alpha1.LogicalClusterName)
				if err != nil && !errors.IsNotFound(err) {
					return nil, err
				} else if errors.IsNotFound(err) {
					return global.Cluster(logicalCluster).Get(corev1alpha1.LogicalClusterName)
				}
				return obj, nil
			},
			delegate: delegate,
		}
	}
}

type requiredGroupsAuthorizer struct {
	getLogicalCluster func(logicalCluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	delegate          authorizer.Authorizer
}

func (a *requiredGroupsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return DelegateAuthorization("deep SAR request", a.delegate).Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	if sets.New[string](attr.GetUser().GetGroups()...).Has(bootstrap.SystemLogicalClusterAdmin) {
		return DelegateAuthorization("logical cluster admin access", a.delegate).Authorize(ctx, attr)
	}

	switch {
	case validation.IsServiceAccount(attr.GetUser()):
		// service accounts are always allowed
		return DelegateAuthorization("service account access to logical cluster", a.delegate).Authorize(ctx, attr)

	default:
		// get logical cluster with required group annotation
		logicalCluster, err := a.getLogicalCluster(cluster.Name)
		if err != nil {
			if errors.IsNotFound(err) {
				return authorizer.DecisionNoOpinion, "logical cluster not found", nil
			}
			return authorizer.DecisionNoOpinion, "", err
		}

		// always let external-logical-cluster-admins through
		if sets.New[string](attr.GetUser().GetGroups()...).Has(bootstrap.SystemExternalLogicalClusterAdmin) {
			return DelegateAuthorization("external logical cluster admin access", a.delegate).Authorize(ctx, attr)
		}

		// check required groups
		value, found := logicalCluster.Annotations[RequiredGroupsAnnotationKey]
		if !found {
			return DelegateAuthorization("logical cluster does not require groups", a.delegate).Authorize(ctx, attr)
		}
		disjunctiveClauses := append(strings.Split(value, ";"), bootstrap.SystemKcpAdminGroup, bootstrap.SystemKcpWorkspaceBootstrapper)
		for _, set := range disjunctiveClauses {
			groups := strings.Split(set, ",")
			if sets.New[string](attr.GetUser().GetGroups()...).HasAll(groups...) {
				return DelegateAuthorization(fmt.Sprintf("user is member of required groups: %s", logicalCluster.Annotations[RequiredGroupsAnnotationKey]), a.delegate).Authorize(ctx, attr)
			}
		}

		return authorizer.DecisionDeny, fmt.Sprintf("user is not a member of required groups: %s", logicalCluster.Annotations[RequiredGroupsAnnotationKey]), nil
	}
}
