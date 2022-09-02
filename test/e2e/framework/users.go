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
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
)

// AdmitWorkspaceAccess create RBAC rules that allow the given users and/or groups to access the given, fully-qualified workspace, i.e.
// the RBAC objects are create in its parent.
func AdmitWorkspaceAccess(t *testing.T, ctx context.Context, kubeClusterClient kubernetesclient.Interface, orgClusterName logicalcluster.Name, users []string, groups []string, verbs []string) {
	parent, hasParent := orgClusterName.Parent()
	require.True(t, hasParent, "org cluster %s should have a parent", orgClusterName)

	if len(groups) > 0 {
		t.Logf("Giving groups %v member access to workspace %q in %q", groups, orgClusterName.Base(), parent)
	}
	if len(users) > 0 {
		t.Logf("Giving users %v member access to workspace %q in %q", users, orgClusterName.Base(), parent)
	}

	roleName := orgClusterName.Base() + "-" + strings.Join(verbs, "-")
	_, err := kubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, parent), &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         verbs,
				Resources:     []string{"workspaces/content"},
				ResourceNames: []string{orgClusterName.Base()},
				APIGroups:     []string{"tenancy.kcp.dev"},
			},
			{
				Verbs:         []string{"get"},
				Resources:     []string{"workspaces"},
				ResourceNames: []string{orgClusterName.Base()},
				APIGroups:     []string{"tenancy.kcp.dev"},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: roleName,
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

	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, parent), binding, metav1.CreateOptions{})
	require.NoError(t, err)
}
