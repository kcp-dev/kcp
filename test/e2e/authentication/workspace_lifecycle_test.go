/*
Copyright 2026 The kcp Authors.

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xrstf/mockoidc"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/authfixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const (
	testOIDCUser  = "user"
	testOIDCEmail = "user@example.com"
)

// oidcWorkspace is a workspace admitting tokens of mock via a WAC.
type oidcWorkspace struct {
	mock *mockoidc.MockOIDC
	// typePath is the workspace holding the WorkspaceType and WAC.
	typePath   logicalcluster.Path
	typeName   string
	authConfig string
	path       logicalcluster.Path
}

// setupOIDCWorkspace creates a WAC and WorkspaceType in typePath
// and a workspace of that type below it, granting cluster-admin to testOIDCEmail.
func setupOIDCWorkspace(
	t *testing.T,
	server kcptestingserver.RunningServer,
	kcpClusterClient kcpclientset.ClusterInterface,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	typePath logicalcluster.Path,
) oidcWorkspace {
	t.Helper()

	t.Logf("Starting mock OIDC issuer and creating WAC and WorkspaceType in %s...", typePath)
	mock, ca := authfixtures.StartMockOIDC(t, server)
	authConfig := authfixtures.CreateWorkspaceOIDCAuthentication(t, t.Context(), kcpClusterClient, typePath, mock, ca, nil)
	typeName := authfixtures.CreateWorkspaceType(t, t.Context(), kcpClusterClient, typePath, "with-oidc", authConfig)

	path, _ := kcptesting.NewWorkspaceFixture(t, server, typePath, kcptesting.WithType(typePath, tenancyv1alpha1.WorkspaceTypeName(typeName)))

	t.Logf("Granting cluster-admin in %s to oidc:%s, the user authenticated by the WAC...", path, testOIDCEmail)
	authfixtures.GrantWorkspaceAccess(t, t.Context(), kubeClusterClient, path, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:" + testOIDCEmail,
	}})

	return oidcWorkspace{
		mock:       mock,
		typePath:   typePath,
		typeName:   typeName,
		authConfig: authConfig,
		path:       path,
	}
}

// requireEventuallyAdmitted waits until token can list ConfigMaps in path.
func requireEventuallyAdmitted(t *testing.T, server kcptestingserver.RunningServer, path logicalcluster.Path, token string) {
	t.Helper()

	client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, server.BaseConfig(t)))
	require.NoError(t, err)

	t.Logf("Waiting for token to be admitted to %s...", path)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := client.Cluster(path).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 500*time.Millisecond)
}

// requireEventuallyUnauthorized waits until token is rejected with 401 in path.
func requireEventuallyUnauthorized(t *testing.T, server kcptestingserver.RunningServer, path logicalcluster.Path, token string) {
	t.Helper()

	client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, server.BaseConfig(t)))
	require.NoError(t, err)

	t.Logf("Waiting for token to be rejected in %s...", path)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := client.Cluster(path).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		require.True(c, apierrors.IsUnauthorized(err), "expected 401, got: %v", err)
	}, wait.ForeverTestTimeout, 500*time.Millisecond)
}

func TestWorkspaceOIDCRevocation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	kcpConfig := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	testcases := map[string]struct {
		revoke func(ctx context.Context, t *testing.T, ws oidcWorkspace)
	}{
		"WorkspaceType no longer references WAC": {
			revoke: func(ctx context.Context, t *testing.T, ws oidcWorkspace) {
				t.Helper()

				t.Logf("Removing WAC %s from WorkspaceType %s", ws.authConfig, ws.typeName)
				wsType, err := kcpClusterClient.Cluster(ws.typePath).TenancyV1alpha1().WorkspaceTypes().Get(ctx, ws.typeName, metav1.GetOptions{})
				require.NoError(t, err)
				wsType.Spec.AuthenticationConfigurations = nil
				_, err = kcpClusterClient.Cluster(ws.typePath).TenancyV1alpha1().WorkspaceTypes().Update(ctx, wsType, metav1.UpdateOptions{})
				require.NoError(t, err)
			},
		},
		"WAC issuer changed": {
			revoke: func(ctx context.Context, t *testing.T, ws oidcWorkspace) {
				t.Helper()

				t.Logf("Pointing WAC %s to a different issuer, so tokens of the original issuer are no longer valid", ws.authConfig)
				otherMock, otherCA := authfixtures.StartMockOIDC(t, server)
				wac, err := kcpClusterClient.Cluster(ws.typePath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Get(ctx, ws.authConfig, metav1.GetOptions{})
				require.NoError(t, err)
				wac.Spec.JWT = []tenancyv1alpha1.JWTAuthenticator{authfixtures.MockJWTAuthenticator(t, otherMock, otherCA, "oidc:", "oidc:")}
				_, err = kcpClusterClient.Cluster(ws.typePath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Update(ctx, wac, metav1.UpdateOptions{})
				require.NoError(t, err)
			},
		},
		"WAC deleted": {
			revoke: func(ctx context.Context, t *testing.T, ws oidcWorkspace) {
				t.Helper()

				t.Logf("Deleting WAC %s", ws.authConfig)
				err := kcpClusterClient.Cluster(ws.typePath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Delete(ctx, ws.authConfig, metav1.DeleteOptions{})
				require.NoError(t, err)
			},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			typePath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithNamePrefix("oidc-revoke"))
			ws := setupOIDCWorkspace(t, server, kcpClusterClient, kubeClusterClient, typePath)

			t.Log("Sending a valid token, so the shard builds and caches the authenticator")
			token := authfixtures.CreateOIDCToken(t, ws.mock, testOIDCUser, testOIDCEmail, nil)
			requireEventuallyAdmitted(t, server, ws.path, token)

			t.Log("Revoking the token's access by changing the objects the cached authenticator was built from")
			tc.revoke(t.Context(), t, ws)

			t.Log("Expecting 401: the shard detects the change on the next request and rebuilds the authenticator without the revoked issuer")
			requireEventuallyUnauthorized(t, server, ws.path, token)
		})
	}
}

func TestWorkspaceOIDCExpiredToken(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	kcpConfig := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	typePath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithNamePrefix("oidc-expired"))
	ws := setupOIDCWorkspace(t, server, kcpClusterClient, kubeClusterClient, typePath)

	t.Log("Sending a valid token first, validating the setup")
	validToken := authfixtures.CreateOIDCToken(t, ws.mock, testOIDCUser, testOIDCEmail, nil)
	requireEventuallyAdmitted(t, server, ws.path, validToken)

	t.Log("Sending a token of the same issuer that expired an hour ago")
	expiredToken := authfixtures.CreateOIDCTokenWithExpiry(t, ws.mock, testOIDCUser, testOIDCEmail, nil, mockoidc.NowFunc().Add(-time.Hour))
	client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(expiredToken, kcpConfig))
	require.NoError(t, err)

	_, err = client.Cluster(ws.path).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
	require.True(t, apierrors.IsUnauthorized(err), "expected 401, got: %v", err)
}
