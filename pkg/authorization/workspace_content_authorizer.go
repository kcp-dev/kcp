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
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	kaudit "k8s.io/apiserver/pkg/audit"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	WorkspaceAccessNotPermittedReason = "workspace access not permitted"

	WorkspaceContentAuditPrefix   = "content.authorization.kcp.dev/"
	WorkspaceContentAuditDecision = WorkspaceContentAuditPrefix + "decision"
	WorkspaceContentAuditReason   = WorkspaceContentAuditPrefix + "reason"
)

var decisionStrings = map[authorizer.Decision]string{
	authorizer.DecisionNoOpinion: "no opinion",
	authorizer.DecisionAllow:     "allow",
	authorizer.DecisionDeny:      "deny",
}

func NewWorkspaceContentAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory, thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &workspaceContentAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		thisWorkspaceLister:      thisWorkspaceLister,

		delegate: delegate,
	}
}

type workspaceContentAuthorizer struct {
	roleLister               rbacv1listers.RoleClusterLister
	roleBindingLister        rbacv1listers.RoleBindingClusterLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	clusterRoleLister        rbacv1listers.ClusterRoleClusterLister
	thisWorkspaceLister      tenancyv1alpha1listers.ThisWorkspaceClusterLister

	// union of local and bootstrap authorizer
	delegate authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	// empty or non-root based workspaces have no meaning in the context of authorizing workspace content.
	if cluster == nil || cluster.Name.Empty() || !cluster.Name.HasPrefix(tenancyv1alpha1.RootCluster) {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "empty or non root workspace",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	isAuthenticated := sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated")
	isUser := len(subjectClusters) == 0
	isServiceAccountFromCluster := subjectClusters[cluster.Name]

	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		attr := deepCopyAttributes(attr)
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		if isAuthenticated && !isUser && !isServiceAccountFromCluster {
			// service accounts from other workspaces might conflict with local service accounts by name.
			// This could lead to unwanted side effects of unwanted applied permissions.
			// Hence, these requests have to be anonymized.
			attr.User = &user.DefaultInfo{
				Name:   "system:anonymous",
				Groups: []string{"system:authenticated"},
			}
		}
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionAllowed,
			WorkspaceContentAuditReason, "deep SAR request",
		)
		return a.delegate.Authorize(ctx, attr)
	}

	// always let logical-cluster-admins through
	if isUser && sets.NewString(attr.GetUser().GetGroups()...).Has(bootstrap.SystemLogicalClusterAdmin) {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionAllowed,
			WorkspaceContentAuditReason, "subject is a logical cluster admin",
		)
		return a.delegate.Authorize(ctx, attr)
	}

	// check the workspace even exists
	this, err := a.thisWorkspaceLister.Cluster(cluster.Name).Get(tenancyv1alpha1.ThisWorkspaceName)
	if err != nil {
		if errors.IsNotFound(err) {
			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, DecisionDenied,
				WorkspaceContentAuditReason, "thisworkspace not found",
			)
			return authorizer.DecisionDeny, WorkspaceAccessNotPermittedReason, nil
		}

		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, fmt.Sprintf("error getting clusterworkspace: %v", err),
		)
		return authorizer.DecisionNoOpinion, "", err
	}

	if this.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing && this.Status.Phase != tenancyv1alpha1.WorkspacePhaseReady {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, fmt.Sprintf("not permitted due to phase %q", this.Status.Phase),
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	switch {
	case !isUser && !isServiceAccountFromCluster:
		// service accounts from other workspaces cannot access
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionDenied,
			WorkspaceContentAuditReason, fmt.Sprintf("service account from other workspace: %v", subjectClusters),
		)
		return authorizer.DecisionDeny, WorkspaceAccessNotPermittedReason, nil

	case isServiceAccountFromCluster:
		// A service account declared in the requested workspace is authorized inside that workspace.

	case isUser:
		authz := rbac.New(
			&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
				a.roleLister.Cluster(cluster.Name),
				a.roleLister.Cluster(genericcontrolplane.LocalAdminCluster),
			)},
			&rbac.RoleBindingLister{Lister: a.roleBindingLister.Cluster(cluster.Name)},
			&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
				a.clusterRoleLister.Cluster(cluster.Name),
				a.clusterRoleLister.Cluster(genericcontrolplane.LocalAdminCluster),
			)},
			&rbac.ClusterRoleBindingLister{Lister: rbacwrapper.NewMergedClusterRoleBindingLister(
				a.clusterRoleBindingLister.Cluster(cluster.Name),
				a.clusterRoleBindingLister.Cluster(genericcontrolplane.LocalAdminCluster),
			)},
		)

		workspaceAttr := authorizer.AttributesRecord{
			User:            attr.GetUser(),
			Verb:            "access",
			Path:            "/",
			ResourceRequest: false,
		}

		dec, reason, err := authz.Authorize(ctx, workspaceAttr)
		if err != nil {
			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, DecisionNoOpinion,
				WorkspaceContentAuditReason, fmt.Sprintf("errors from workspace content authorizer: %v", err),
			)
			return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, err
		}
		if dec != authorizer.DecisionAllow {
			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, decisionStrings[dec],
				WorkspaceContentAuditReason, reason,
			)
			return dec, WorkspaceAccessNotPermittedReason, nil
		}
	}

	kaudit.AddAuditAnnotations(
		ctx,
		WorkspaceContentAuditDecision, DecisionAllowed,
		WorkspaceContentAuditReason, "allowed by workspace content authorizer",
	)

	return a.delegate.Authorize(ctx, attr)
}
