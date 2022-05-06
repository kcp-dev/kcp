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

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyv1 "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

func NewWorkspaceContentAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, clusterWorkspaceLister tenancyv1.ClusterWorkspaceLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &workspaceContentAuthorizer{
		versionedInformers: versionedInformers,

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
	clusterWorkspaceLister   tenancyv1.ClusterWorkspaceLister

	// TODO: this will go away when scoping lands. Then we only have those 4 listers above.
	versionedInformers clientgoinformers.SharedInformerFactory

	// union of local and bootstrap authorizer
	delegate authorizer.Authorizer
}

func (a *workspaceContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("%q workspace access not permitted", cluster.Name), err
	}
	if cluster == nil || cluster.Name.Empty() || !cluster.Name.HasPrefix(v1alpha1.RootCluster) {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("%q workspace access not permitted", cluster.Name), nil
	}

	clusterWorkspace := cluster.Name.Base()
	subjectClusters := map[logicalcluster.LogicalCluster]bool{}
	for _, sc := range attr.GetUser().GetExtra()[authserviceaccount.ClusterNameKey] {
		subjectClusters[logicalcluster.New(sc)] = true
	}

	// everybody authenticated has access to the root workspace, for service account only when there defined
	if cluster.Name == v1alpha1.RootCluster {
		if sets.NewString(attr.GetUser().GetGroups()...).Has("system:authenticated") && (len(subjectClusters) == 0 || subjectClusters[v1alpha1.RootCluster]) {
			return a.delegate.Authorize(ctx, attributesWithReplacedGroups(attr, append(attr.GetUser().GetGroups(), bootstrap.SystemKcpClusterWorkspaceAccessGroup)))
		}
		return authorizer.DecisionNoOpinion, fmt.Sprintf("%q workspace access not permitted", cluster.Name), err
	}
	parentClusterName, _ := cluster.Name.Parent()

	parentWorkspaceKubeInformer := rbacwrapper.FilterInformers(parentClusterName, a.versionedInformers.Rbac().V1())
	parentAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: parentWorkspaceKubeInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: parentWorkspaceKubeInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: parentWorkspaceKubeInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: parentWorkspaceKubeInformer.ClusterRoleBindings().Lister()},
	)

	extraGroups := sets.NewString()

	// check the workspace even exists
	if ws, err := a.clusterWorkspaceLister.Get(clusters.ToClusterAwareKey(parentClusterName, clusterWorkspace)); err != nil {
		if errors.IsNotFound(err) {
			return authorizer.DecisionDeny, fmt.Sprintf("%q workspace access not permitted", cluster.Name), nil
		}
		return authorizer.DecisionNoOpinion, "", err
	} else if len(ws.Status.Initializers) > 0 {
		// service accounts can potentially access sub-workspaces, but not other workspaces during initialization. Hence, these two case:
		// 1. it's  not a serivce account
		// 2. it's a service account, and it is defined in the parent workspace
		if len(subjectClusters) == 0 && subjectClusters[parentClusterName] {
			workspaceAttr := authorizer.AttributesRecord{
				User:            attr.GetUser(),
				Verb:            "initialize",
				APIGroup:        v1alpha1.SchemeGroupVersion.Group,
				APIVersion:      v1alpha1.SchemeGroupVersion.Version,
				Resource:        "clusterworkspaces",
				Subresource:     "content",
				Name:            clusterWorkspace,
				ResourceRequest: true,
			}
			dec, reason, err := parentAuthorizer.Authorize(ctx, workspaceAttr)
			if err != nil {
				return dec, reason, err
			}
			if dec == authorizer.DecisionAllow {
				extraGroups.Insert(bootstrap.SystemKcpClusterWorkspaceAccessGroup)
			}
		}
	} else {
		if subjectClusters[cluster.Name] {
			// a subject from a workspace, like a ServiceAccount, is automatically authenticated
			// against that workspace. On the other hand, referencing that in the parent workspace for further permissions
			// is not possible. Hence, we skip the authorization steps for the verb below.
			extraGroups.Insert(bootstrap.SystemKcpClusterWorkspaceAccessGroup)
		}

		// service accounts can get access or be admin in sub-workspaces, but not other workspaces
		if len(subjectClusters) == 0 && subjectClusters[parentClusterName] {
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
					APIGroup:        v1alpha1.SchemeGroupVersion.Group,
					APIVersion:      v1alpha1.SchemeGroupVersion.Version,
					Resource:        "clusterworkspaces",
					Subresource:     "content",
					Name:            clusterWorkspace,
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
	}

	if len(extraGroups) == 0 {
		return authorizer.DecisionNoOpinion, fmt.Sprintf("%q workspace access not permitted", cluster.Name), nil
	}

	return a.delegate.Authorize(ctx, attributesWithReplacedGroups(attr, append(attr.GetUser().GetGroups(), extraGroups.List()...)))
}

func attributesWithReplacedGroups(attr authorizer.Attributes, groups []string) authorizer.Attributes {
	return authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   attr.GetUser().GetName(),
			UID:    attr.GetUser().GetUID(),
			Groups: groups,
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
