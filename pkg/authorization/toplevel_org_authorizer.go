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
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	kaudit "k8s.io/apiserver/pkg/audit"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/client"
	tenancyv1beta1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1beta1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	TopLevelContentAuditPrefix   = "toplevel.authorization.kcp.dev/"
	TopLevelContentAuditDecision = TopLevelContentAuditPrefix + "decision"
	TopLevelContentAuditReason   = TopLevelContentAuditPrefix + "reason"
)

// NewTopLevelOrganizationAccessAuthorizer returns an authorizer that checks for access+member verb in
// clusterworkspaces/content of the top-level workspace the request workspace is nested in. If one of
// these verbs are admitted, the delegate authorizer is called. Otherwise, NoOpionion is returned if
// the top-level workspace exists, and Deny otherwise.
func NewTopLevelOrganizationAccessAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory, workspaceLister tenancyv1beta1listers.WorkspaceLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &topLevelOrgAccessAuthorizer{
		rootAuthorizer: rbac.New(
			&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
				versionedInformers.Rbac().V1().Roles().Lister().Cluster(tenancyv1alpha1.RootCluster),
				versionedInformers.Rbac().V1().Roles().Lister().Cluster(genericcontrolplane.LocalAdminCluster),
			)},
			&rbac.RoleBindingLister{Lister: versionedInformers.Rbac().V1().RoleBindings().Lister().Cluster(tenancyv1alpha1.RootCluster)},
			&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
				versionedInformers.Rbac().V1().ClusterRoles().Lister().Cluster(tenancyv1alpha1.RootCluster),
				versionedInformers.Rbac().V1().ClusterRoles().Lister().Cluster(genericcontrolplane.LocalAdminCluster),
			)},
			&rbac.ClusterRoleBindingLister{Lister: rbacwrapper.NewMergedClusterRoleBindingLister(
				versionedInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(tenancyv1alpha1.RootCluster),
				versionedInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(genericcontrolplane.LocalAdminCluster),
			)},
		),
		workspaceLister: workspaceLister,
		delegate:        delegate,
	}
}

type topLevelOrgAccessAuthorizer struct {
	rootAuthorizer  *rbac.RBACAuthorizer
	workspaceLister tenancyv1beta1listers.WorkspaceLister
	delegate        authorizer.Authorizer
}

func (a *topLevelOrgAccessAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionAllowed,
			TopLevelContentAuditReason, "deep SAR request",
		)
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return a.delegate.Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, "empty cluster name",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	if !cluster.Name.HasPrefix(tenancyv1alpha1.RootCluster) {
		// nobody other than system:masters (excluded from authz) has access to workspaces not based in root
		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, "non-root prefixed workspace access not permitted",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	isAuthenticated := sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated")
	isUser := len(subjectClusters) == 0
	isServiceAccount := len(subjectClusters) > 0
	isServiceAccountFromRootCluster := subjectClusters[tenancyv1alpha1.RootCluster]

	// Every authenticated user has access to the root workspace but not every service account.
	// For root, only service accounts declared in root have access.
	if cluster.Name == tenancyv1alpha1.RootCluster {
		if isAuthenticated && (isUser || isServiceAccountFromRootCluster) {
			return a.delegate.Authorize(ctx, attr)
		}
		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, "root workspace access by non-root service account not permitted",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
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

	// get org in the root
	requestTopLevelOrgName, ok := topLevelOrg(cluster.Name)
	if !ok {
		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, "not part of root workspace hierarchy",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	}

	// check the org workspace exists in the root workspace
	topLevelWSKey := client.ToClusterAwareKey(tenancyv1alpha1.RootCluster, requestTopLevelOrgName)
	if _, err := a.workspaceLister.Get(topLevelWSKey); err != nil {
		if errors.IsNotFound(err) {
			kaudit.AddAuditAnnotations(
				ctx,
				TopLevelContentAuditDecision, DecisionDenied,
				TopLevelContentAuditReason, fmt.Sprintf("clusterworkspace %q not found", topLevelWSKey),
			)
			return authorizer.DecisionDeny, WorkspaceAccessNotPermittedReason, nil
		}

		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, fmt.Sprintf("error getting clusterworkspace %q: %v", topLevelWSKey, err),
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, err
	}

	switch {
	case isServiceAccount:
		// service account will automatically get access to its top-level org
		for sc := range subjectClusters {
			subjectTopLevelOrg, ok := topLevelOrg(sc)
			if !ok {
				continue
			}
			if subjectTopLevelOrg == requestTopLevelOrgName {
				kaudit.AddAuditAnnotations(
					ctx,
					TopLevelContentAuditDecision, DecisionAllowed,
					TopLevelContentAuditReason, "serviceaccount belongs to this top level workspace hierarchy",
				)
				return a.delegate.Authorize(ctx, attr)
			}
		}

		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionNoOpinion,
			TopLevelContentAuditReason, "serviceaccount does not belong to this top level workspace hierarchy",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
	case isUser:
		workspaceAttr := authorizer.AttributesRecord{
			User:            attr.GetUser(),
			Verb:            "access",
			APIGroup:        tenancyv1beta1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1beta1.SchemeGroupVersion.Version,
			Resource:        "workspaces",
			Subresource:     "content",
			Name:            requestTopLevelOrgName,
			ResourceRequest: true,
		}

		dec, reason, err := a.rootAuthorizer.Authorize(ctx, workspaceAttr)
		if err != nil {
			kaudit.AddAuditAnnotations(
				ctx,
				TopLevelContentAuditDecision, DecisionNoOpinion,
				TopLevelContentAuditReason, fmt.Sprintf(`error in root workspace RBAC, verb="access" resource="workspaces/content", name=%q, reason=%q, error=%v`, requestTopLevelOrgName, reason, err))
			return authorizer.DecisionNoOpinion, reason, err
		}
		if dec == authorizer.DecisionAllow {
			kaudit.AddAuditAnnotations(
				ctx,
				TopLevelContentAuditDecision, DecisionAllowed,
				TopLevelContentAuditReason, fmt.Sprintf(`allowed by root workspace RBAC, verb="access" resource="workspaces/content", name=%q, reason=%q`, requestTopLevelOrgName, reason),
			)
			return a.delegate.Authorize(ctx, attr)
		}

		kaudit.AddAuditAnnotations(
			ctx,
			TopLevelContentAuditDecision, DecisionString(dec),
			TopLevelContentAuditReason, fmt.Sprintf(`forbidden by root workspace RBAC, verb="access" resource="workspaces/content", name=%q, reason=%q`, requestTopLevelOrgName, reason),
		)
	}

	return authorizer.DecisionNoOpinion, WorkspaceAccessNotPermittedReason, nil
}

func topLevelOrg(clusterName logicalcluster.Name) (string, bool) {
	for {
		parent, hasParent := clusterName.Parent()
		if !hasParent {
			// apparently not under `root`
			return "", false
		}
		if parent == tenancyv1alpha1.RootCluster {
			return clusterName.Base(), true
		}
		clusterName = parent
	}
}
