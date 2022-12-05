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

package framework

import (
	"context"
	"fmt"
	"testing"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// AdmitWorkspaceAccess create RBAC rules that allow the given users and/or groups to access the given workspace, optionally as admin.
func AdmitWorkspaceAccess(t *testing.T, ctx context.Context, kubeClusterClient kcpkubernetesclientset.ClusterInterface, clusterName logicalcluster.Name, users []string, groups []string, admin bool) {
	if len(groups) > 0 {
		t.Logf("Giving groups %v member access to workspace %q", groups, clusterName)
	}
	if len(users) > 0 {
		t.Logf("Giving users %v member access to workspace %q", users, clusterName)
	}

	nameSuffix := "access"
	roleName := bootstrap.SystemKcpWorkspaceAccessGroup
	if admin {
		nameSuffix = "admin"
		roleName = "cluster-admin"
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("workspace-%s-", nameSuffix),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     roleName,
		},
	}

	for _, group := range groups {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Group",
			Name:     group,
		})
	}
	for _, user := range users {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "User",
			Name:     user,
		})
	}

	_, err := kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
}
