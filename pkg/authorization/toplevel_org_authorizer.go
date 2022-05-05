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

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1 "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

// NewTopLevelOrganizationAccessAuthorizer returns an authorizer that checks for access+member verb in
// clusterworkspaces/content of the top-level workspace the request workspace is nested in. If one of
// these verbs are admitted, the delegate authorizer is called. Otherwise, NoOpionion is returned if
// the top-level workspace exists, and Deny otherwise.
func NewTopLevelOrganizationAccessAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, clusterWorkspaceLister tenancyv1.ClusterWorkspaceLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	rootKubeInformer := rbacwrapper.FilterInformers(v1alpha1.RootCluster, versionedInformers.Rbac().V1())

	return &topLevelOrgAccessAuthorizer{
		rootAuthorizer: rbac.New(
			&rbac.RoleGetter{Lister: rootKubeInformer.Roles().Lister()},
			&rbac.RoleBindingLister{Lister: rootKubeInformer.RoleBindings().Lister()},
			&rbac.ClusterRoleGetter{Lister: rootKubeInformer.ClusterRoles().Lister()},
			&rbac.ClusterRoleBindingLister{Lister: rootKubeInformer.ClusterRoleBindings().Lister()},
		),
		clusterWorkspaceLister: clusterWorkspaceLister,
		delegate:               delegate,
	}
}

type topLevelOrgAccessAuthorizer struct {
	rootAuthorizer         *rbac.RBACAuthorizer
	clusterWorkspaceLister tenancyv1.ClusterWorkspaceLister
	delegate               authorizer.Authorizer
}

func (a *topLevelOrgAccessAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil || cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "workspace access not permitted", err
	}

	workspaceAccessNotPermittedReason := fmt.Sprintf("%q workspace access not permitted", cluster.Name)
	if !cluster.Name.HasPrefix(v1alpha1.RootCluster) {
		// nobody other than system:masters (excluded from authz) has access to workspaces not based in root
		return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, nil
	}

	// everybody authenticated has access to the root workspace
	if cluster.Name == v1alpha1.RootCluster {
		if sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated") {
			return a.delegate.Authorize(ctx, attr)
		}
		return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, nil
	}

	// get org in the root
	requestTopLevelOrg, ok := topLevelOrg(cluster.Name)
	if !ok {
		return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, nil
	}

	// check the org in the root exists
	if _, err := a.clusterWorkspaceLister.Get(clusters.ToClusterAwareKey(v1alpha1.RootCluster, requestTopLevelOrg)); err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, workspaceAccessNotPermittedReason, nil
		}
		return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, err
	}

	if subjectCluster := attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey]; len(subjectCluster) > 0 {
		// service account will automatically get access to its top-level org
		subjectTopLevelOrg, ok := topLevelOrg(logicalcluster.New(subjectCluster[0]))
		if !ok || subjectTopLevelOrg != requestTopLevelOrg {
			return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, nil
		}
		return a.delegate.Authorize(ctx, attr)
	} else {
		var (
			errList    []error
			reasonList []string
		)
		for _, verb := range []string{"access", "member"} {
			workspaceAttr := authorizer.AttributesRecord{
				User:        attr.GetUser(),
				Verb:        verb,
				APIGroup:    v1alpha1.SchemeGroupVersion.Group,
				APIVersion:  v1alpha1.SchemeGroupVersion.Version,
				Resource:    "clusterworkspaces",
				Subresource: "content",
				Name:        requestTopLevelOrg,

				ResourceRequest: true,
			}

			dec, reason, err := a.rootAuthorizer.Authorize(ctx, workspaceAttr)
			if err != nil {
				errList = append(errList, err)
				reasonList = append(reasonList, reason)
				continue
			}
			if dec == authorizer.DecisionAllow {
				return a.delegate.Authorize(ctx, attr)
			}
		}
		if len(errList) > 0 {
			return authorizer.DecisionNoOpinion, strings.Join(reasonList, "\n"), utilerrors.NewAggregate(errList)
		}
	}

	return authorizer.DecisionNoOpinion, workspaceAccessNotPermittedReason, nil
}

func topLevelOrg(clusterName logicalcluster.Name) (string, bool) {
	for {
		parent, hasParent := clusterName.Parent()
		if !hasParent {
			// apparently not under `root`
			return "", false
		}
		if parent == v1alpha1.RootCluster {
			return clusterName.Base(), true
		}
		clusterName = parent
	}
}
