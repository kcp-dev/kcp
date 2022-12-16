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
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
)

const (
	RequiredGroupsAuditPrefix   = "requiredgroups.authorization.kcp.dev/"
	RequiredGroupsAuditDecision = RequiredGroupsAuditPrefix + "decision"
	RequiredGroupsAuditReason   = RequiredGroupsAuditPrefix + "reason"

	// RequiredGroupsAnnotationKey is a comma-separated list (OR'ed) of semicolon separated
	// groups (AND'ed) that a user must be a member of to be able to access the workspace.
	RequiredGroupsAnnotationKey = "authorization.kcp.dev/required-groups"
)

// NewRequiredGroupsAuthorizer returns an authorizer that a set of groups stored
// on the LogicalCluster object. Service account by-pass this.
func NewRequiredGroupsAuthorizer(logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &requiredGroupsAuthorizer{
		logicalClusterLister: logicalClusterLister,
		delegate:             delegate,
	}
}

type requiredGroupsAuthorizer struct {
	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister
	delegate             authorizer.Authorizer
}

func (a *requiredGroupsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return a.delegate.Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	subjectClusters := map[logicalcluster.Path]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.NewPath(sc)] = true
	}

	isUser := len(subjectClusters) == 0
	isServiceAccount := len(subjectClusters) > 0

	// always let logical-cluster-admins through
	if isUser && sets.NewString(attr.GetUser().GetGroups()...).Has(bootstrap.SystemLogicalClusterAdmin) {
		return a.delegate.Authorize(ctx, attr)
	}

	switch {
	case isServiceAccount:
		// service accounts are always allowed
		return a.delegate.Authorize(ctx, attr)

	case isUser:
		// get LogicalCluster with required group annotation
		this, err := a.logicalClusterLister.Cluster(cluster.Name).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			if errors.IsNotFound(err) {
				return authorizer.DecisionNoOpinion, "this workspace not found", nil
			}
			return authorizer.DecisionNoOpinion, "", err
		}

		// check required groups
		value, found := this.Annotations[RequiredGroupsAnnotationKey]
		if !found {
			return a.delegate.Authorize(ctx, attr)
		}
		disjunctiveClauses := append(strings.Split(value, ","), bootstrap.SystemKcpAdminGroup, bootstrap.SystemKcpWorkspaceBootstrapper)
		for _, set := range disjunctiveClauses {
			groups := strings.Split(set, ";")
			if sets.NewString(attr.GetUser().GetGroups()...).HasAll(groups...) {
				return a.delegate.Authorize(ctx, attr)
			}
		}

		return authorizer.DecisionDeny, fmt.Sprintf("subject is not a member of required groups: %s", this.Annotations[RequiredGroupsAnnotationKey]), nil
	}

	return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
}
