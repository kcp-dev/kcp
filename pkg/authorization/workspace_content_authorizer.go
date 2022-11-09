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

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	rbacv1listers "github.com/kcp-dev/client-go/listers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	WorkspaceAccessNotPermittedReason = "workspace access not permitted"
)

func NewWorkspaceContentAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory, clusterWorkspaceLister tenancyv1alpha1listers.ClusterWorkspaceClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &workspaceContentAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		clusterWorkspaceLister:   clusterWorkspaceLister,
		delegate:                 delegate,
	}
}

type workspaceContentAuthorizer struct {
	roleLister               rbacv1listers.RoleClusterLister
	roleBindingLister        rbacv1listers.RoleBindingClusterLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	clusterRoleLister        rbacv1listers.ClusterRoleClusterLister
	clusterWorkspaceLister   tenancyv1alpha1listers.ClusterWorkspaceClusterLister
	delegate                 authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	// empty or non-root based workspaces have no meaning in the context of authorizing workspace content.
	if cluster == nil || cluster.Name.Empty() || !cluster.Name.HasPrefix(tenancyv1alpha1.RootCluster) {
		return authorizer.DecisionNoOpinion, "empty or non root workspace", nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	isAuthenticated := sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated")
	isUser := len(subjectClusters) == 0
	isServiceAccountFromRootCluster := subjectClusters[tenancyv1alpha1.RootCluster]
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
		return a.delegate.Authorize(ctx, attr)
	}

	// Every authenticated user has access to the root workspace but not every service account.
	// For root, only service accounts declared in root have access.
	if cluster.Name == tenancyv1alpha1.RootCluster {
		if isAuthenticated && (isUser || isServiceAccountFromRootCluster) {
			withGroups := deepCopyAttributes(attr)
			withGroups.User.(*user.DefaultInfo).Groups = append(attr.GetUser().GetGroups(), bootstrap.SystemKcpClusterWorkspaceAccessGroup)
			return a.delegate.Authorize(ctx, withGroups)
		}
		return authorizer.DecisionNoOpinion, "root workspace access by non-root service account not permitted", nil
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

	// non-root workspaces must have a parent
	parentClusterName, hasParent := cluster.Name.Parent()
	if !hasParent {
		return authorizer.DecisionNoOpinion, "non-root workspace that does not have a parent", nil
	}

	parentAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
			a.roleLister.Cluster(parentClusterName),
			a.roleLister.Cluster(genericcontrolplane.LocalAdminCluster),
		)},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Cluster(parentClusterName)},
		&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
			a.clusterRoleLister.Cluster(parentClusterName),
			a.clusterRoleLister.Cluster(genericcontrolplane.LocalAdminCluster),
		)},
		&rbac.ClusterRoleBindingLister{Lister: rbacwrapper.NewMergedClusterRoleBindingLister(
			a.clusterRoleBindingLister.Cluster(parentClusterName),
			a.clusterRoleBindingLister.Cluster(genericcontrolplane.LocalAdminCluster),
		)},
	)

	extraGroups := sets.NewString()

	// check the workspace even exists
	ws, err := a.clusterWorkspaceLister.Cluster(parentClusterName).Get(cluster.Name.Base())
	if err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, "clusterworkspace not found", nil
		}
		return authorizer.DecisionNoOpinion, "error getting clusterworkspace", err
	}

	if ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing && ws.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("not permitted due to phase %q", ws.Status.Phase), nil
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
				APIGroup:        tenancyv1beta1.SchemeGroupVersion.Group,
				APIVersion:      tenancyv1beta1.SchemeGroupVersion.Version,
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
			return authorizer.DecisionNoOpinion, strings.Join(reasonList, "\n"), utilerrors.NewAggregate(errList)
		}
	}

	// non-admin subjects don't have access to initializing workspaces.
	if ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing && !extraGroups.Has(bootstrap.SystemKcpClusterWorkspaceAdminGroup) {
		return authorizer.DecisionNoOpinion, "not permitted, clusterworkspace is in initializing phase", nil
	}

	if len(extraGroups) == 0 {
		return authorizer.DecisionNoOpinion, "not permitted, subject has not been granted any groups", nil
	}

	withGroups := deepCopyAttributes(attr)
	withGroups.User.(*user.DefaultInfo).Groups = append(attr.GetUser().GetGroups(), extraGroups.List()...)

	return a.delegate.Authorize(ctx, withGroups)
}

func deepCopyAttributes(attr authorizer.Attributes) *authorizer.AttributesRecord {
	return &authorizer.AttributesRecord{
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
