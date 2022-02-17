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
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	frameworkrbac "github.com/kcp-dev/kcp/pkg/virtual/framework/rbac"
)

func NewBootstrapPolicyAuthorizer(informers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	filteredInformer := frameworkrbac.FilterPerCluster("system:admin", informers.Rbac().V1())

	a := rbac.New(
		&rbac.RoleGetter{Lister: filteredInformer.Roles().Lister()},
		&rbac.RoleBindingLister{Lister: filteredInformer.RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: filteredInformer.ClusterRoles().Lister()},
		&rbac.ClusterRoleBindingLister{Lister: filteredInformer.ClusterRoleBindings().Lister()},
	)

	return a, a
}
