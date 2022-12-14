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
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

const (
	WorkspaceAccessNotPermittedReason = "workspace access not permitted"
)

func NewWorkspaceContentAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory, logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &workspaceContentAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		logicalClusterLister:     logicalClusterLister,

		delegate: delegate,
	}
}

type workspaceContentAuthorizer struct {
	roleLister               rbacv1listers.RoleClusterLister
	roleBindingLister        rbacv1listers.RoleBindingClusterLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	clusterRoleLister        rbacv1listers.ClusterRoleClusterLister
	logicalClusterLister     corev1alpha1listers.LogicalClusterClusterLister

	delegate authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	cluster := genericapirequest.ClusterFrom(ctx)

	// empty or non-root based workspaces have no meaning in the context of authorizing workspace content.
	if cluster == nil || cluster.Name.Empty() || strings.HasPrefix(cluster.Name.String(), "system:") {
		return authorizer.DecisionNoOpinion, "empty or system workspace", nil
	}

	subjectClusters := map[logicalcluster.Name]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.Name(sc)] = true
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
		return DelegateAuthorization("deep SAR request", a.delegate).Authorize(ctx, attr)
	}

	// always let logical-cluster-admins through
	if isUser && sets.NewString(attr.GetUser().GetGroups()...).Has(bootstrap.SystemLogicalClusterAdmin) {
		return DelegateAuthorization("logical cluster admin access", a.delegate).Authorize(ctx, attr)
	}

	// check the workspace even exists
	logicalCluster, err := a.logicalClusterLister.Cluster(cluster.Name).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, "LogicalCluster not found", nil
		}
		return authorizer.DecisionNoOpinion, "error getting LogicalCluster", err
	}

	if logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing && logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("not permitted due to phase %q", logicalCluster.Status.Phase), nil
	}

	switch {
	case !isUser && !isServiceAccountFromCluster:
		// service accounts from other workspaces cannot access
		return authorizer.DecisionDeny, "foreign service account", nil

	case isServiceAccountFromCluster:
		// A service account declared in the requested workspace is authorized inside that workspace.
		return DelegateAuthorization("service account access to thisworkspace", a.delegate).Authorize(ctx, attr)

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

		dec, _, err := authz.Authorize(ctx, workspaceAttr)
		if err != nil {
			return authorizer.DecisionNoOpinion, fmt.Sprintf("errors from workspace content authorizer: %v", err), err
		}
		if dec != authorizer.DecisionAllow {
			return dec, "no verb=access permission on /", nil
		}
		return DelegateAuthorization("user access to thisworkspace", a.delegate).Authorize(ctx, attr)
	}

	return authorizer.DecisionNoOpinion, "unknown user type", nil
}
