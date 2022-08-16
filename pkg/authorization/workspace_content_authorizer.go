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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	kaudit "k8s.io/apiserver/pkg/audit"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1 "k8s.io/client-go/informers/rbac/v1"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyalphav1 "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	WorkspaceAcccessNotPermittedReason = "workspace access not permitted"

	DecisionNoOpinion = "NoOpinion"
	DecisionAllowed   = "Allowed"
	DecisionDenied    = "Denied"

	WorkspaceContentAuditPrefix   = "content.authorization.kcp.dev/"
	WorkspaceContentAuditDecision = WorkspaceContentAuditPrefix + "decision"
	WorkspaceContentAuditReason   = WorkspaceContentAuditPrefix + "reason"
)

func NewWorkspaceContentAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, clusterWorkspaceLister tenancyalphav1.ClusterWorkspaceLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &workspaceContentAuthorizer{
		rbacInformers: versionedInformers.Rbac().V1(),

		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		clusterWorkspaceLister:   clusterWorkspaceLister,

		delegate: delegate,
	}
}

type workspaceContentAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister
	clusterWorkspaceLister   tenancyalphav1.ClusterWorkspaceLister

	// TODO: this will go away when scoping lands. Then we only have those 4 listers above.
	rbacInformers rbacv1.Interface

	// union of local and bootstrap authorizer
	delegate authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, fmt.Sprintf("error getting cluster from request: %v", err),
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, err
	}
	// empty or non-root based workspaces have no meaning in the context of authorizing workspace content.
	if cluster == nil || cluster.Name.Empty() || !cluster.Name.HasPrefix(tenancyv1alpha1.RootCluster) {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "empty or non root workspace",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	isAuthenticated := sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated")
	isUser := len(subjectClusters) == 0
	isServiceAccountFromRootCluster := subjectClusters[tenancyv1alpha1.RootCluster]
	isServiceAccountFromCluster := subjectClusters[cluster.Name]

	// Every authenticated user has access to the root workspace but not every service account.
	// For root, only service accounts declared in root have access.
	if cluster.Name == tenancyv1alpha1.RootCluster {
		if isAuthenticated && (isUser || isServiceAccountFromRootCluster) {
			withGroups := deepCopyAttributes(attr)
			withGroups.User.(*user.DefaultInfo).Groups = append(attr.GetUser().GetGroups(), bootstrap.SystemKcpClusterWorkspaceAccessGroup)

			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, DecisionAllowed,
				WorkspaceContentAuditReason, "subject is either an authenticated user or a serviceaccount in root",
			)

			return a.delegate.Authorize(ctx, withGroups)
		}
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "root workspace access by non-root service account not permitted",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, err
	}

	// non-root workspaces must have a parent
	parentClusterName, hasParent := cluster.Name.Parent()
	if !hasParent {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "non-root workspace that does not have a parent",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, nil
	}

	parentWorkspaceKubeInformer := rbacwrapper.FilterInformers(parentClusterName, a.rbacInformers)
	bootstrapInformer := rbacwrapper.FilterInformers(genericcontrolplane.LocalAdminCluster, a.rbacInformers)

	mergedClusterRoleInformer := rbacwrapper.MergedClusterRoleInformer(parentWorkspaceKubeInformer.ClusterRoles(), bootstrapInformer.ClusterRoles())
	mergedRoleInformer := rbacwrapper.MergedRoleInformer(parentWorkspaceKubeInformer.Roles(), bootstrapInformer.Roles())
	mergedClusterRoleBindingsInformer := rbacwrapper.MergedClusterRoleBindingInformer(parentWorkspaceKubeInformer.ClusterRoleBindings(), bootstrapInformer.ClusterRoleBindings())

	parentAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: mergedRoleInformer.Lister()},
		&rbac.RoleBindingLister{Lister: parentWorkspaceKubeInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: mergedClusterRoleInformer.Lister()},
		&rbac.ClusterRoleBindingLister{Lister: mergedClusterRoleBindingsInformer.Lister()},
	)

	extraGroups := sets.NewString()

	// check the workspace even exists
	ws, err := a.clusterWorkspaceLister.Get(clusters.ToClusterAwareKey(parentClusterName, cluster.Name.Base()))
	if err != nil {
		if errors.IsNotFound(err) {
			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, DecisionDenied,
				WorkspaceContentAuditReason, "clusterworkspace not found",
			)
			return authorizer.DecisionDeny, WorkspaceAcccessNotPermittedReason, nil
		}

		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, fmt.Sprintf("error getting clusterworkspace: %v", err),
		)
		return authorizer.DecisionNoOpinion, "", err
	}

	if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing && ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, fmt.Sprintf("not permitted due to phase %q", ws.Status.Phase),
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, nil
	}

	switch {
	case isServiceAccountFromCluster:
		// A service account declared in the requested workspace is authorized inside that workspace.
		// Referencing such a service account in the parent workspace is not possible,
		// hence authorization against "admin" or "access" verbs in the parent is not possible either.
		extraGroups.Insert(bootstrap.SystemKcpClusterWorkspaceAccessGroup)

	case isUser:
		verbToGroupMembership := map[string][]string{
			"admin":  {bootstrap.SystemKcpClusterWorkspaceAccessGroup, bootstrap.SystemKcpClusterWorkspaceAdminGroup},
			"access": {bootstrap.SystemKcpClusterWorkspaceAccessGroup},
		}

		var (
			errList    []error
			reasonList []string
		)
		for verb, groups := range verbToGroupMembership {
			workspaceAttr := authorizer.AttributesRecord{
				User:            attr.GetUser(),
				Verb:            verb,
				APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
				APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
				Resource:        "workspaces",
				Subresource:     "content",
				Name:            cluster.Name.Base(),
				ResourceRequest: true,
			}

			dec, reason, err := parentAuthorizer.Authorize(ctx, workspaceAttr)
			if err != nil {
				errList = append(errList, err)
				reasonList = append(reasonList, reason)
				continue
			}
			if dec == authorizer.DecisionAllow {
				extraGroups.Insert(groups...)
			}
		}
		if len(errList) > 0 {
			kaudit.AddAuditAnnotations(
				ctx,
				WorkspaceContentAuditDecision, DecisionNoOpinion,
				WorkspaceContentAuditReason, fmt.Sprintf("errors from parent authorizer: %v", utilerrors.NewAggregate(errList)),
			)
			return authorizer.DecisionNoOpinion, strings.Join(reasonList, "\n"), utilerrors.NewAggregate(errList)
		}
	}

	// non-admin subjects don't have access to initializing workspaces.
	if ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing && !extraGroups.Has(bootstrap.SystemKcpClusterWorkspaceAdminGroup) {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "not permitted, clusterworkspace is in initializing phase",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, nil
	}

	if len(extraGroups) == 0 {
		kaudit.AddAuditAnnotations(
			ctx,
			WorkspaceContentAuditDecision, DecisionNoOpinion,
			WorkspaceContentAuditReason, "not permitted, subject has not been granted any groups",
		)
		return authorizer.DecisionNoOpinion, WorkspaceAcccessNotPermittedReason, nil
	}

	withGroups := deepCopyAttributes(attr)
	withGroups.User.(*user.DefaultInfo).Groups = append(attr.GetUser().GetGroups(), extraGroups.List()...)

	kaudit.AddAuditAnnotations(
		ctx,
		WorkspaceContentAuditDecision, DecisionAllowed,
		WorkspaceContentAuditReason, fmt.Sprintf("allowed with additional groups: %v", extraGroups),
	)

	return a.delegate.Authorize(ctx, withGroups)
}

func deepCopyAttributes(attr authorizer.Attributes) authorizer.AttributesRecord {
	return authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   attr.GetUser().GetName(),
			UID:    attr.GetUser().GetUID(),
			Groups: attr.GetUser().GetGroups(),
			Extra:  attr.GetUser().GetExtra(),
		},
		Verb:            attr.GetVerb(),
		Namespace:       attr.GetNamespace(),
		APIGroup:        attr.GetAPIGroup(),
		APIVersion:      attr.GetAPIVersion(),
		Resource:        attr.GetResource(),
		Subresource:     attr.GetSubresource(),
		Name:            attr.GetName(),
		ResourceRequest: attr.IsResourceRequest(),
		Path:            attr.GetPath(),
	}
}
