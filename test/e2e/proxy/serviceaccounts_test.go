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

package proxy

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestServiceAccountsThroughFrontProxy checks that the front-proxy itself
// authenticates service account tokens of workspaces on every shard. The
// sharded-test-server gives the proxy the service account signing key, so a
// token is validated by the proxy, which looks the service account and its
// token secret up on the shard hosting the workspace through its workspace
// index, without a connection to the root shard. For a workspace pinned to
// each shard, a legacy and a bound token are sent through the proxy, which
// must report the service account identity and let it act with its RBAC;
// once the token secret is gone, both tokens must be rejected.
func TestServiceAccountsThroughFrontProxy(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	if len(server.ShardNames()) < 2 {
		t.Skip("This test requires a multi-shard setup with a front-proxy.")
	}

	// every request below goes through the front-proxy
	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	for _, shard := range server.ShardNames() {
		t.Run(shard, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(shard))

			t.Log("Creating namespace, role and role binding for the default service account")
			namespace, err := kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-proxy-sa-"},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create namespace")
			_, err = kubeClusterClient.Cluster(wsPath).RbacV1().Roles(namespace.Name).Create(ctx, &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: "configmaps"},
				Rules:      []rbacv1.PolicyRule{{Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"configmaps"}}},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create role")
			_, err = kubeClusterClient.Cluster(wsPath).RbacV1().RoleBindings(namespace.Name).Create(ctx, &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "configmaps"},
				Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "default", Namespace: namespace.Name}},
				RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "configmaps", APIGroup: rbacv1.GroupName},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create role binding")

			t.Log("Waiting for the default service account")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(namespace.Name).Get(ctx, "default", metav1.GetOptions{})
				return err == nil, fmt.Sprintf("%v", err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "the default service account was not created")

			t.Log("Creating the legacy token secret and waiting for it to be filled")
			_, err = kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(namespace.Name).Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "default-token", Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}},
				Type:       corev1.SecretTypeServiceAccountToken,
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create the token secret")
			var tokenSecret *corev1.Secret
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				s, err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(namespace.Name).Get(ctx, "default-token", metav1.GetOptions{})
				if err != nil {
					return false, err.Error()
				}
				tokenSecret = s
				return len(s.Data[corev1.ServiceAccountTokenKey]) > 0, "token not filled yet"
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "the token secret was not filled")

			t.Log("Requesting a bound token")
			boundToken, err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(namespace.Name).CreateToken(ctx, "default", &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{"https://kcp.default.svc"},
					ExpirationSeconds: ptr.To[int64](3600),
					BoundObjectRef:    &authenticationv1.BoundObjectReference{APIVersion: "v1", Kind: "Secret", Name: tokenSecret.Name, UID: tokenSecret.UID},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create a bound token")

			tokens := map[string]string{
				"legacy": string(tokenSecret.Data[corev1.ServiceAccountTokenKey]),
				"bound":  boundToken.Status.Token,
			}
			expectedUser := "system:serviceaccount:" + namespace.Name + ":default"
			clients := map[string]kcpkubernetesclientset.ClusterInterface{}
			for kind, token := range tokens {
				client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, cfg))
				require.NoError(t, err)
				clients[kind] = client
			}

			for kind, client := range clients {
				t.Logf("The %s token authenticates through the front-proxy as the service account", kind)
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					review, err := client.Cluster(wsPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
					if err != nil {
						return false, err.Error()
					}
					return review.Status.UserInfo.Username == expectedUser, fmt.Sprintf("authenticated as %q", review.Status.UserInfo.Username)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "the %s token did not authenticate as %s", kind, expectedUser)

				t.Logf("The %s token acts with the service account's RBAC", kind)
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					_, err := client.Cluster(wsPath).CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
					return err == nil, fmt.Sprintf("%v", err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "the %s token could not list configmaps", kind)
				_, err := client.Cluster(wsPath).CoreV1().Secrets(namespace.Name).List(ctx, metav1.ListOptions{})
				require.True(t, apierrors.IsForbidden(err), "the %s token must not list secrets, got: %v", kind, err)
			}

			t.Log("Deleting the token secret invalidates both tokens")
			require.NoError(t, kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(namespace.Name).Delete(ctx, tokenSecret.Name, metav1.DeleteOptions{}))
			for kind, client := range clients {
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					_, err := client.Cluster(wsPath).CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
					return apierrors.IsUnauthorized(err), fmt.Sprintf("%v", err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "the %s token was still accepted after its secret was deleted", kind)
			}
		})
	}
}
