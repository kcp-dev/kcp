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

package authentication

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceOIDC(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx := context.Background()
	rootPath := logicalcluster.NewPath("root")

	// start kcp and setup clients
	server := kcptesting.SharedKcpServer(t)

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	// start a mock OIDC server that will listen on a random port
	// (only for discovery and keyset handling, no actual login workflows)
	m, ca := startMockOIDC(t, server)
	defer m.Shutdown() //nolint:errcheck

	// setup a new workspace auth config that uses mockoidc's server
	authConfig := &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mockoidc",
		},
		Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
			JWT: []tenancyv1alpha1.JWTAuthenticator{
				mockJWTAuthenticator(t, m, ca),
			},
		},
	}

	t.Log("Creating WorkspaceAuthenticationConfguration...")
	_, err = kcpClusterClient.Cluster(rootPath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Create(ctx, authConfig, metav1.CreateOptions{})
	require.NoError(t, err)

	// use this config in a new WorkspaceType
	wsType := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "with-oidc",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			AuthenticationConfigurations: []tenancyv1alpha1.AuthenticationConfigurationReference{{
				Configuration: authConfig.Name,
			}},
		},
	}

	t.Log("Creating WorkspaceType...")
	_, err = kcpClusterClient.Cluster(rootPath).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wsType, metav1.CreateOptions{})
	require.NoError(t, err)

	// create a new workspace with our new type
	t.Log("Creating Workspace...")
	team1Path, _ := kcptesting.NewWorkspaceFixture(t, server, rootPath, kcptesting.WithName("team1"), kcptesting.WithType(rootPath, tenancyv1alpha1.WorkspaceTypeName(wsType.Name)))

	// grant permissions to OIDC user
	email := "test@example.com"
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "allow-oidc-user",
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "cluster-admin",
		},
		Subjects: []rbacv1.Subject{{
			Kind: "User",
			Name: fmt.Sprintf("oidc:%s", email),
		}, {
			Kind: "Group",
			Name: "oidc:developers",
		}},
	}

	t.Log("Grating OIDC permissions...")
	_, err = kubeClusterClient.Cluster(team1Path).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)

	// sanity check: owner can access their own workspace
	t.Log("Owner should be allowed to list ConfigMaps.")
	_, err = kubeClusterClient.Cluster(team1Path).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// random user with random token should have no access
	randoKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken("invalid-token", kcpConfig))
	require.NoError(t, err)

	t.Log("An unauthenticated user shouldn't be able to list ConfigMaps.")
	_, err = randoKubeClusterClient.Cluster(team1Path).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	// A user with a valid token from the issuer however should be allowed in.
	// Test that both group and user claims work as expected.
	adminToken := createOIDCToken(t, m, "the-admin", email, []string{"admins"})
	devToken := createOIDCToken(t, m, "the-dev", "developer@example.com", []string{"developers"})

	t.Logf("OIDC-authenticated users should be able to list ConfigMaps.")

	for _, token := range []string{adminToken, devToken} {
		authenticatedKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
		require.NoError(t, err)

		// Initialization of the JWT authenticator happens asynchronously and depends both
		// on the cluster index knowing the WorkspaceType and the authenticator itself
		// performing OIDC discovery.

		require.Eventually(t, func() bool {
			_, err := authenticatedKubeClusterClient.Cluster(team1Path).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})

			return err == nil
		}, wait.ForeverTestTimeout, 500*time.Millisecond)
	}
}
