/*
Copyright 2021 The KCP Authors.

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

package rbac

import (
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacauthorizer "k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

func NewSubjectLocator(cluster logicalcluster.Name, informers kcprbacinformers.ClusterInterface) rbacauthorizer.SubjectLocator {
	return rbacauthorizer.NewSubjectAccessEvaluator(
		&rbacauthorizer.RoleGetter{Lister: informers.Roles().Lister().Cluster(cluster)},
		&rbacauthorizer.RoleBindingLister{Lister: informers.RoleBindings().Lister().Cluster(cluster)},
		&rbacauthorizer.ClusterRoleGetter{Lister: informers.ClusterRoles().Lister().Cluster(cluster)},
		&rbacauthorizer.ClusterRoleBindingLister{Lister: informers.ClusterRoleBindings().Lister().Cluster(cluster)},
		"",
	)
}
