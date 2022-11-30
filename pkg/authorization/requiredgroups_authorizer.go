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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	kaudit "k8s.io/apiserver/pkg/audit"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
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
// on the ThisWorkspace object. Service account by-pass this.
func NewRequiredGroupsAuthorizer(thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &requiredGroupsAuthorizer{
		thisWorkspaceLister: thisWorkspaceLister,
		delegate:            delegate,
	}
}

type requiredGroupsAuthorizer struct {
	thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister
	delegate            authorizer.Authorizer
}

func (a *requiredGroupsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		kaudit.AddAuditAnnotations(
			ctx,
			RequiredGroupsAuditDecision, DecisionAllowed,
			RequiredGroupsAuditReason, "deep SAR request",
		)
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return a.delegate.Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		kaudit.AddAuditAnnotations(
			ctx,
			RequiredGroupsAuditDecision, DecisionNoOpinion,
			RequiredGroupsAuditReason, "empty cluster name",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	isUser := len(subjectClusters) == 0
	isServiceAccount := len(subjectClusters) > 0

	// always let logical-cluster-admins through
	if isUser && sets.NewString(attr.GetUser().GetGroups()...).Has(bootstrap.SystemLogicalClusterAdmin) {
		kaudit.AddAuditAnnotations(
			ctx,
			RequiredGroupsAuditDecision, DecisionAllowed,
			RequiredGroupsAuditReason, "subject is a logical cluster admin",
		)
		return a.delegate.Authorize(ctx, attr)
	}

	switch {
	case isServiceAccount:
		// service accounts are always allowed
		kaudit.AddAuditAnnotations(
			ctx,
			RequiredGroupsAuditDecision, DecisionAllowed,
			RequiredGroupsAuditReason, "subject is a service account",
		)
		return a.delegate.Authorize(ctx, attr)

	case isUser:
		// get ThisWorkspace with required group annotation
		this, err := a.thisWorkspaceLister.Cluster(cluster.Name).Get(tenancyv1alpha1.ThisWorkspaceName)
		if err != nil {
			if errors.IsNotFound(err) {
				kaudit.AddAuditAnnotations(
					ctx,
					RequiredGroupsAuditDecision, DecisionNoOpinion,
					RequiredGroupsAuditReason, "this workspace not found",
				)
				return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
			}
			return authorizer.DecisionNoOpinion, "", err
		}

		// check required groups
		value, found := this.Annotations[RequiredGroupsAnnotationKey]
		if !found {
			kaudit.AddAuditAnnotations(
				ctx,
				RequiredGroupsAuditDecision, DecisionAllowed,
				RequiredGroupsAuditReason, "no required groups annotation",
			)
			return a.delegate.Authorize(ctx, attr)
		}
		disjunctiveClauses := append(strings.Split(value, ","), bootstrap.SystemKcpAdminGroup, bootstrap.SystemKcpWorkspaceBootstrapper)
		for _, set := range disjunctiveClauses {
			groups := strings.Split(set, ";")
			if sets.NewString(attr.GetUser().GetGroups()...).HasAll(groups...) {
				kaudit.AddAuditAnnotations(
					ctx,
					RequiredGroupsAuditDecision, DecisionAllowed,
					RequiredGroupsAuditReason, "subject is a member of required groups",
				)
				return a.delegate.Authorize(ctx, attr)
			}
		}

		// matching clause found
		kaudit.AddAuditAnnotations(
			ctx,
			RequiredGroupsAuditDecision, DecisionDenied,
			RequiredGroupsAuditReason, fmt.Sprintf("subject is not a member of required groups: %s", this.Annotations[RequiredGroupsAnnotationKey]),
		)
		return authorizer.DecisionDeny, WorkspaceAccessNotPermittedReason, nil
	}

	return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
}
