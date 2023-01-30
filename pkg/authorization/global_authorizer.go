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
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	rbacwrapper "github.com/kcp-dev/kcp/pkg/virtual/framework/wrappers/rbac"
)

type GlobalAuthorizer struct {
	newAuthorizer func(clusterName logicalcluster.Name) authorizer.Authorizer
}

func NewGlobalAuthorizer(localKubeInformers, globalKubeInformers kcpkubernetesinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &GlobalAuthorizer{
		newAuthorizer: func(clusterName logicalcluster.Name) authorizer.Authorizer {
			return rbac.New(
				&rbac.RoleGetter{Lister: rbacwrapper.NewMergedRoleLister(
					globalKubeInformers.Rbac().V1().Roles().Lister().Cluster(clusterName),
					localKubeInformers.Rbac().V1().Roles().Lister().Cluster(genericcontrolplane.LocalAdminCluster),
				)},
				&rbac.RoleBindingLister{Lister: globalKubeInformers.Rbac().V1().RoleBindings().Lister().Cluster(clusterName)},
				&rbac.ClusterRoleGetter{Lister: rbacwrapper.NewMergedClusterRoleLister(
					globalKubeInformers.Rbac().V1().ClusterRoles().Lister().Cluster(clusterName),
					localKubeInformers.Rbac().V1().ClusterRoles().Lister().Cluster(genericcontrolplane.LocalAdminCluster),
				)},
				&rbac.ClusterRoleBindingLister{Lister: globalKubeInformers.Rbac().V1().ClusterRoleBindings().Lister().Cluster(clusterName)},
			)
		},
	}

	return a, a
}

func (a *GlobalAuthorizer) RulesFor(ctx context.Context, user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	// TODO: wire context in RulesFor interface
	panic("implement me")
}

func (a *GlobalAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	scopedAuth := a.newAuthorizer(cluster.Name)
	dec, reason, err := scopedAuth.Authorize(ctx, attr)
	if err != nil {
		err = fmt.Errorf("error authorizing global policy for cluster %q: %w", cluster.Name, err)
	}
	return dec, fmt.Sprintf("global cluster %q policy: %v", cluster.Name, reason), err
}
