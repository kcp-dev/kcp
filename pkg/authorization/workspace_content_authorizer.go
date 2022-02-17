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
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1 "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
)

func NewWorkspaceContentAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, workspaceLister tenancyv1.ClusterWorkspaceLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &OrgWorkspaceAuthorizer{
		versionedInformers: versionedInformers,

		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		workspaceLister:          workspaceLister,

		delegate: delegate,
	}
}

type OrgWorkspaceAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister
	workspaceLister          tenancyv1.ClusterWorkspaceLister

	// TODO: this will go away when scoping lands. Then we only have those 4 listers above.
	versionedInformers clientgoinformers.SharedInformerFactory

	// union of local and bootstrap authorizer
	delegate authorizer.Authorizer
}

func (a *OrgWorkspaceAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}
	if cluster == nil || cluster.Name == "" {
		return authorizer.DecisionNoOpinion, "", nil
	}

	parentClusterName, err := helper.ParentClusterName(cluster.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	_, workspace, err := helper.ParseLogicalClusterName(cluster.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	orgWorkspaceKubeInformer := frameworkrbac.FilterPerCluster(parentClusterName, a.versionedInformers.Rbac().V1())
	orgAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: orgWorkspaceKubeInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: orgWorkspaceKubeInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: orgWorkspaceKubeInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: orgWorkspaceKubeInformer.ClusterRoleBindings().Lister()},
	)

	// TODO: decide if we want to require workspaces for all kcp variations. For now, only check if the workspace controllers are running,
	// as that ensures the ClusterWorkspace CRD is installed, and that our shared informer factory can sync all its caches successfully.
	if a.workspaceLister != nil {
		// check the workspace even exists
		// TODO: using scoping when available
		if ws, err := a.workspaceLister.Get(clusters.ToClusterAwareKey(parentClusterName, workspace)); err != nil {
			if errors.IsNotFound(err) {
				return authorizer.DecisionDeny, "WorkspaceDoesNotExist", nil
			}
			return authorizer.DecisionNoOpinion, "", err
		} else if len(ws.Status.Initializers) > 0 {
			workspaceAttr := authorizer.AttributesRecord{
				User:            attr.GetUser(),
				Verb:            attr.GetVerb(),
				APIGroup:        v1alpha1.SchemeGroupVersion.Group,
				APIVersion:      v1alpha1.SchemeGroupVersion.Version,
				Resource:        "workspaces",
				Subresource:     "initialize",
				Name:            workspace,
				ResourceRequest: true,
			}

			dec, reason, err := orgAuthorizer.Authorize(ctx, workspaceAttr)
			if err != nil {
				return dec, reason, err
			}
			if dec != authorizer.DecisionAllow {
				return dec, "workspace is initializing", nil
			}
		}
	}

	verbToGroupMembership := map[string]string{
		"admin":  "system:kcp:workspace:admin",
		"edit":   "system:kcp:workspace:edit",
		"view":   "system:kcp:workspace:view",
		"access": "system:kcp:authenticated",
	}

	extraGroups := []string{}
	var (
		errList    []error
		reasonList []string
	)
	for verb, group := range verbToGroupMembership {
		workspaceAttr := authorizer.AttributesRecord{
			User:            attr.GetUser(),
			Verb:            verb,
			APIGroup:        v1alpha1.SchemeGroupVersion.Group,
			APIVersion:      v1alpha1.SchemeGroupVersion.Version,
			Resource:        "workspaces",
			Subresource:     "content",
			Name:            workspace,
			ResourceRequest: true,
		}

		dec, reason, err := orgAuthorizer.Authorize(ctx, workspaceAttr)
		if err != nil {
			errList = append(errList, err)
			reasonList = append(reasonList, reason)
			continue
		}
		if dec == authorizer.DecisionAllow {
			extraGroups = append(extraGroups, group)
		}
	}
	if len(errList) > 0 {
		return authorizer.DecisionNoOpinion, strings.Join(reasonList, "\n"), utilerrors.NewAggregate(errList)
	}
	if len(extraGroups) == 0 {
		return authorizer.DecisionNoOpinion, "workspace access not permitted", nil
	}

	attrsWithExtraGroups := authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   attr.GetUser().GetName(),
			UID:    attr.GetUser().GetUID(),
			Groups: append(attr.GetUser().GetGroups(), extraGroups...),
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

	return a.delegate.Authorize(ctx, attrsWithExtraGroups)
}
