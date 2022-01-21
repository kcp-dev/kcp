/*
Copyright The KCP Authors.

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

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
)

func NewWorkspaceContentAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &OrgWorkspaceAuthorizer{
		versionedInformers: versionedInformers,

		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),

		delegate: delegate,
	}
}

type OrgWorkspaceAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister

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
	reqScope := cluster.Name

	// future: when we have org workspaces, after Steve's proxy PR merged
	// Probably like system:org-replica:reqScope.Name().
	// TODO: not completely true: we will have a (partial) replica of the org workspace on this shard, which holds the necessary
	//       RBAC objects to do this very authorization work.
	// For now, "admin" is our org workspace:
	orgWorkspace := "admin"

	orgWorkspaceInformer := frameworkrbac.FilterPerCluster(orgWorkspace, a.versionedInformers.Rbac().V1())
	orgAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: orgWorkspaceInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: orgWorkspaceInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: orgWorkspaceInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: orgWorkspaceInformer.ClusterRoleBindings().Lister()},
	)

	verbToGroupMembership := map[string]string{
		"admin":   "cluster-admin",
		"edit":    "system:kcp:workspace:edit",
		"view":    "system:kcp:workspace:view",
		"default": "",
	}

	extraGroups := []string{}
	var (
		errList    []error
		reasonList []string
	)
	for verb, group := range verbToGroupMembership {
		workspaceAttr := authorizer.AttributesRecord{
			User:        attr.GetUser(),
			Verb:        verb,
			APIGroup:    v1alpha1.SchemeGroupVersion.Group,
			APIVersion:  v1alpha1.SchemeGroupVersion.Version,
			Resource:    "workspaces",
			Subresource: "content", Name: reqScope, // TODO: parse and remove org prefix as soon as Steve's proxy PR merges
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

	klog.Infof("adding groups: %v", extraGroups)

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
