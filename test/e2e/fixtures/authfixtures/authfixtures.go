/*
Copyright 2025 The KCP Authors.

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

package authfixtures

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

func CreateWorkspaceType(t *testing.T, ctx context.Context, client kcpclientset.ClusterInterface, workspace logicalcluster.Path, name string, authConfigNames ...string) string {
	configs := []tenancyv1alpha1.AuthenticationConfigurationReference{}
	for _, name := range authConfigNames {
		configs = append(configs, tenancyv1alpha1.AuthenticationConfigurationReference{
			Name: name,
		})
	}

	// setup a new workspace auth config that uses mockoidc's server
	wsType := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			AuthenticationConfigurations: configs,
		},
	}

	t.Logf("Creating WorkspaceType %s...", name)
	_, err := client.Cluster(workspace).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wsType, metav1.CreateOptions{})
	require.NoError(t, err)

	return name
}

func GrantWorkspaceAccess(t *testing.T, ctx context.Context, client kcpkubernetesclientset.ClusterInterface, workspace logicalcluster.Path, name, clusterRole string, subjects []rbacv1.Subject) {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: clusterRole,
		},
		Subjects: subjects,
	}

	t.Log("Creating ClusterRoleBinding...")
	_, err := client.Cluster(workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
}
