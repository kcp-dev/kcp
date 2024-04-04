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
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

type LocalAuthorizer struct {
	roleLister               rbacv1listers.RoleClusterLister
	roleBindingLister        rbacv1listers.RoleBindingClusterLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingClusterLister
	clusterRoleLister        rbacv1listers.ClusterRoleClusterLister
}

func NewLocalAuthorizer(versionedInformers kcpkubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &LocalAuthorizer{
		// listers are saved in the struct here to ensure that informers are instantiated early and we do not encounter race conditions with starting them.
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
	}

	return a, a
}

func (a *LocalAuthorizer) RulesFor(ctx context.Context, user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return nil, nil, false, fmt.Errorf("empty cluster name")
	}

	scopedAuth := a.newAuthorizer(cluster.Name)
	return scopedAuth.RulesFor(ctx, user, namespace)
}

func (a *LocalAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	scopedAuth := a.newAuthorizer(cluster.Name)
	dec, reason, err := scopedAuth.Authorize(ctx, attr)
	if err != nil {
		err = fmt.Errorf("error authorizing local policy for cluster %q: %w", cluster.Name, err)
	}

	return dec, fmt.Sprintf("local cluster %q policy: %v", cluster.Name, reason), err
}

func (a *LocalAuthorizer) newAuthorizer(clusterName logicalcluster.Name) *rbac.RBACAuthorizer {
	return rbac.New(
		&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
			a.roleLister.Cluster(clusterName),
			a.roleLister.Cluster(controlplaneapiserver.LocalAdminCluster),
		)},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Cluster(clusterName)},
		&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
			a.clusterRoleLister.Cluster(clusterName),
			a.clusterRoleLister.Cluster(controlplaneapiserver.LocalAdminCluster),
		)},
		&rbac.ClusterRoleBindingLister{Lister: a.clusterRoleBindingLister.Cluster(clusterName)},
	)
}
