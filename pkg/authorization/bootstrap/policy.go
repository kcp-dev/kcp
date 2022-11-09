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

package bootstrap

import (
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	rbacv1helpers "k8s.io/kubernetes/pkg/apis/rbac/v1"
	rbacrest "k8s.io/kubernetes/pkg/registry/rbac/rest"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac/bootstrappolicy"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

const (
	// SystemKcpClusterWorkspaceAccessGroup is a group that gives a user basic access to a workspace.
	// It does not give them any permissions in the workspace.
	SystemKcpClusterWorkspaceAccessGroup = "system:kcp:clusterworkspace:access"
	// SystemKcpClusterWorkspaceAdminGroup is an admin group per cluster workspace. Members of this group have all permissions
	// in the referenced cluster workspace (capped by maximal permission policy).
	SystemKcpClusterWorkspaceAdminGroup = "system:kcp:clusterworkspace:admin"
	// SystemKcpAdminGroup is global admin group. Members of this group have all permissions across all cluster workspaces.
	SystemKcpAdminGroup = "system:kcp:admin"
	// SystemKcpWorkspaceBootstrapper is the group used to bootstrap resources, both during the root setup, as well
	// as when the default APIBinding initializing controller performs its bootstrapping for initializing workspaces.
	// We need a separate group (not system:masters) for this because system-owned workspaces (e.g. root:users) need
	// a workspace owner annotation set, and the owner annotation is skipped/not set for system:masters.
	SystemKcpWorkspaceBootstrapper = "system:kcp:tenancy:workspace-bootstrapper"
	// SystemLogicalClusterAdmin is a group used by the scheduler to create ThisWorkspaces resources.
	// This group allows it to skip the entire authorization stack except the bootstrap policy authorizer.
	// Otherwise, access to a top level org or a parent workspace would be required.
	SystemLogicalClusterAdmin = "system:kcp:logical-cluster-admin"
)

// ClusterRoleBindings return default rolebindings to the default roles
func clusterRoleBindings() []rbacv1.ClusterRoleBinding {
	return []rbacv1.ClusterRoleBinding{
		clusterRoleBindingCustomName(rbacv1helpers.NewClusterBinding("cluster-admin").Groups(SystemKcpClusterWorkspaceAdminGroup, SystemKcpAdminGroup).BindingOrDie(), SystemKcpClusterWorkspaceAdminGroup),
		clusterRoleBindingCustomName(rbacv1helpers.NewClusterBinding("system:kcp:tenancy:reader").Groups(SystemKcpClusterWorkspaceAccessGroup).BindingOrDie(), SystemKcpClusterWorkspaceAccessGroup),
		clusterRoleBindingCustomName(rbacv1helpers.NewClusterBinding(SystemKcpWorkspaceBootstrapper).Groups(SystemKcpWorkspaceBootstrapper, "apis.kcp.dev:binding:system:kcp:tenancy:workspace-bootstrapper").BindingOrDie(), SystemKcpWorkspaceBootstrapper),
		clusterRoleBindingCustomName(rbacv1helpers.NewClusterBinding(SystemLogicalClusterAdmin).Groups(SystemLogicalClusterAdmin).BindingOrDie(), SystemLogicalClusterAdmin),
	}
}

func clusterRoles() []rbacv1.ClusterRole {
	return []rbacv1.ClusterRole{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "system:kcp:tenancy:reader"},
			Rules: []rbacv1.PolicyRule{
				rbacv1helpers.NewRule("list", "watch").Groups(tenancy.GroupName).Resources("workspaces").RuleOrDie(), // "get" is by workspace name through workspace VW
				rbacv1helpers.NewRule(bootstrappolicy.Read...).Groups(tenancy.GroupName).Resources("clusterworkspacetypes").RuleOrDie(),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: SystemKcpWorkspaceBootstrapper},
			Rules: []rbacv1.PolicyRule{
				rbacv1helpers.NewRule("*").Groups("*").Resources("*").RuleOrDie(),
				rbacv1helpers.NewRule("*").URLs("*").RuleOrDie(),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: SystemLogicalClusterAdmin},
			Rules: []rbacv1.PolicyRule{
				rbacv1helpers.NewRule("create, update").Groups(tenancy.GroupName).Resources("thisworkspaces").RuleOrDie(),
			},
		},
	}
}

func clusterRoleBindingCustomName(b rbacv1.ClusterRoleBinding, name string) rbacv1.ClusterRoleBinding {
	b.ObjectMeta.Name = name
	return b
}

func Policy() *rbacrest.PolicyData {
	return &rbacrest.PolicyData{
		ClusterRoles:        clusterRoles(),
		ClusterRoleBindings: clusterRoleBindings(),
	}
}
