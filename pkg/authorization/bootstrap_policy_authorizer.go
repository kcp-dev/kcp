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
	"k8s.io/apiserver/pkg/authorization/authorizer"
	clientgoinformers "k8s.io/client-go/informers"
	kcprbaclister "k8s.io/client-go/kcp/listers/rbac/v1"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

func NewBootstrapPolicyAuthorizer(informers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := rbac.New(
		&rbac.RoleGetter{Lister: informers.Rbac().V1().Roles().Lister().(*kcprbaclister.RoleClusterLister).Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.RoleBindingLister{Lister: informers.Rbac().V1().RoleBindings().Lister().(*kcprbaclister.RoleBindingClusterLister).Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.ClusterRoleGetter{Lister: informers.Rbac().V1().ClusterRoles().Lister().(*kcprbaclister.ClusterRoleClusterLister).Cluster(genericcontrolplane.LocalAdminCluster)},
		&rbac.ClusterRoleBindingLister{Lister: informers.Rbac().V1().ClusterRoleBindings().Lister().(*kcprbaclister.ClusterRoleBindingClusterLister).Cluster(genericcontrolplane.LocalAdminCluster)},
	)

	return a, a
}
