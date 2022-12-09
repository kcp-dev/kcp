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
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

// NewTopLevelOrganizationAccessAuthorizer returns an authorizer that checks for access+member verb in
// clusterworkspaces/content of the top-level workspace the request workspace is nested in. If one of
// these verbs are admitted, the delegate authorizer is called. Otherwise, NoOpionion is returned if
// the top-level workspace exists, and Deny otherwise.
func NewTopLevelOrganizationAccessAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory, clusterWorkspaceLister tenancyv1alpha1listers.ClusterWorkspaceClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
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
		clusterWorkspaceLister: clusterWorkspaceLister,
		delegate:               delegate,
	}
}

type topLevelOrgAccessAuthorizer struct {
	rootAuthorizer         *rbac.RBACAuthorizer
	clusterWorkspaceLister tenancyv1alpha1listers.ClusterWorkspaceClusterLister
	delegate               authorizer.Authorizer
}

func (a *topLevelOrgAccessAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// this is a deep SAR request, we have to skip the checks here and delegate to the subsequent authorizer.
		return a.delegate.Authorize(ctx, attr)
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	if !cluster.Name.HasPrefix(tenancyv1alpha1.RootCluster) {
		// nobody other than system:masters (excluded from authz) has access to workspaces not based in root
		return authorizer.DecisionNoOpinion, "non-root prefixed workspace access not permitted", nil
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
		return authorizer.DecisionNoOpinion, "root workspace access by non-root service account not permitted", nil
	}

	// get org in the root
	requestTopLevelOrgName, ok := topLevelOrg(cluster.Name)
	if !ok {
		return authorizer.DecisionNoOpinion, "not part of root workspace hierarchy", nil
	}

	// check the org workspace exists in the root workspace
	if _, err := a.clusterWorkspaceLister.Cluster(tenancyv1alpha1.RootCluster).Get(requestTopLevelOrgName); err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, fmt.Sprintf("clusterworkspace %s|%s not found", tenancyv1alpha1.RootCluster, requestTopLevelOrgName), nil
		}
		return authorizer.DecisionNoOpinion, fmt.Sprintf("error getting clusterworkspace %s|%s", tenancyv1alpha1.RootCluster, requestTopLevelOrgName), fmt.Errorf("error getting top level org cluster %q: %w", requestTopLevelOrgName, err)
	}

	var noOpinionReason string
	switch {
	case isServiceAccount:
		// service account will automatically get access to its top-level org
		for sc := range subjectClusters {
			subjectTopLevelOrg, ok := topLevelOrg(sc)
			if !ok {
				continue
			}
			if subjectTopLevelOrg == requestTopLevelOrgName {
				return a.delegate.Authorize(ctx, attr)
			}
		}

		noOpinionReason = "serviceaccount does not belong to this top level workspace hierarchy"
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
			return authorizer.DecisionNoOpinion, reason, fmt.Errorf(`error in root workspace RBAC, verb="access" resource="workspaces/content", name=%q: %w`, requestTopLevelOrgName, err)
		}

		if dec == authorizer.DecisionAllow {
			return a.delegate.Authorize(ctx, attr)
		}

		noOpinionReason = fmt.Sprintf(`forbidden by root workspace RBAC, verb="access" resource="workspaces/content", name=%q, reason=%q`, requestTopLevelOrgName, reason)
	}

	return authorizer.DecisionNoOpinion, noOpinionReason, nil
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
