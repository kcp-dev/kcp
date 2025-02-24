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

package authorizer

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	"github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestServiceAccounts(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	orgPath, _ := framework.NewOrganizationFixture(t, server)

	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	// Find two different shards if possible.
	shardA := v1alpha1.RootShard
	shardB := v1alpha1.RootShard
	shards, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	for _, shard := range shards.Items {
		if shard.Name != shardA {
			shardB = shard.Name
			break
		}
	}

	// Cases for different kind of service accounts.
	tokenCases := []struct {
		name  string
		token func(t *testing.T, tokenSecret *corev1.Secret) string
	}{
		{"Legacy token", func(t *testing.T, tokenSecret *corev1.Secret) string {
			t.Log("Waiting for service account secret to be filled")
			require.Eventually(t, func() bool {
				s, err := kubeClusterClient.Cluster(logicalcluster.From(tokenSecret).Path()).CoreV1().Secrets(metav1.NamespaceDefault).Get(ctx, "default-token", metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return false
				} else if err != nil {
					t.Fatalf("unexpected error retrieving service account secret: %v", err)
				}
				tokenSecret = s
				return len(s.Data) > 0
			}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default-token\" secret not filled in namespace %s", metav1.NamespaceDefault)

			return string(tokenSecret.Data["token"])
		}},
		{"Bound service token", func(t *testing.T, tokenSecret *corev1.Secret) string {
			t.Log("Creating service account bound token")
			boundToken, err := kubeClusterClient.Cluster(logicalcluster.From(tokenSecret).Path()).CoreV1().ServiceAccounts(metav1.NamespaceDefault).CreateToken(ctx, "default", &authenticationv1.TokenRequest{
				Spec: authenticationv1.TokenRequestSpec{
					Audiences:         []string{"https://kcp.default.svc"},
					ExpirationSeconds: ptr.To[int64](3600),
					BoundObjectRef: &authenticationv1.BoundObjectReference{
						APIVersion: "v1",
						Kind:       "Secret",
						Name:       tokenSecret.Name,
						UID:        tokenSecret.UID,
					},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "failed to create token")
			return boundToken.Status.Token
		}},
	}

	// Cases for revoking access to the service account.
	revokeCases := []struct {
		name         string
		revoke       func(t *testing.T, wsPath, otherPath logicalcluster.Path)
		revokesLocal bool
	}{{
		name: "revoking by removing other workspace access",
		revoke: func(t *testing.T, wsPath, otherPath logicalcluster.Path) {
			t.Log("Taking away the authenticated access to the other workspace, restricting to only service accounts")
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				crb, err := kubeClusterClient.Cluster(otherPath).RbacV1().ClusterRoleBindings().Get(ctx, "sa-access", metav1.GetOptions{})
				if err != nil {
					return err
				}
				crb.Subjects = []rbacv1.Subject{
					{
						Kind:     "Group",
						APIGroup: "rbac.authorization.k8s.io",
						Name:     "system:serviceaccounts",
					},
				}
				_, err = kubeClusterClient.Cluster(otherPath).RbacV1().ClusterRoleBindings().Update(ctx, crb, metav1.UpdateOptions{})
				return err
			})
			require.NoError(t, err, "failed to update cluster role binding")
		},
	}, {
		name: "revoking by removing the service account",
		revoke: func(t *testing.T, wsPath, otherPath logicalcluster.Path) {
			err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(metav1.NamespaceDefault).Delete(ctx, "default", metav1.DeleteOptions{})
			require.NoError(t, err, "failed to delete service account")
		},
		revokesLocal: true,
	}, {
		name: "revoking by removing the service account secret",
		revoke: func(t *testing.T, wsPath, otherPath logicalcluster.Path) {
			err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(metav1.NamespaceDefault).Delete(ctx, "default-token", metav1.DeleteOptions{})
			require.NoError(t, err, "failed to delete service account secret")
		},
		revokesLocal: true,
	}}

	for i, tokenCase := range tokenCases {
		for _, revokeCase := range revokeCases {
			t.Run(fmt.Sprintf("%s, %s", tokenCase.name, revokeCase.name), func(t *testing.T) {
				t.Parallel()

				t.Log("Create workspace")
				wsPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithShard(shardA))

				t.Log("Creating role to access configmaps")
				_, err = kubeClusterClient.Cluster(wsPath).RbacV1().Roles(metav1.NamespaceDefault).Create(ctx, &rbacv1.Role{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sa-access-configmap",
					},
					Rules: []rbacv1.PolicyRule{
						{
							APIGroups: []string{""},
							Resources: []string{"configmaps"},
							Verbs:     []string{"get", "list", "watch"},
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create role")

				t.Log("Creating role binding to access configmaps")
				_, err = kubeClusterClient.Cluster(wsPath).RbacV1().RoleBindings(metav1.NamespaceDefault).Create(ctx, &rbacv1.RoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "sa-access-configmap",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Name:      "default",
							Namespace: metav1.NamespaceDefault,
						},
					},
					RoleRef: rbacv1.RoleRef{
						Kind:     "Role",
						Name:     "sa-access-configmap",
						APIGroup: rbacv1.GroupName,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create role")

				t.Log("Waiting for service account to be created")
				require.Eventually(t, func() bool {
					_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(metav1.NamespaceDefault).Get(ctx, "default", metav1.GetOptions{})
					if apierrors.IsNotFound(err) {
						return false
					} else if err != nil {
						t.Fatalf("unexpected error retrieving service account: %v", err)
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default\" service account not created in namespace %q", metav1.NamespaceDefault)

				t.Log("Creating the service account secret manually")
				tokenSecret, err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(metav1.NamespaceDefault).Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default-token",
						Annotations: map[string]string{
							corev1.ServiceAccountNameKey: "default",
						},
					},
					Type: corev1.SecretTypeServiceAccountToken,
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create service account secret")

				t.Log("Creating the service account client")
				saRestConfig := framework.ConfigWithToken(tokenCase.token(t, tokenSecret), server.BaseConfig(t))
				saKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(saRestConfig)
				require.NoError(t, err)
				saKubeClient, err := kubernetes.NewForConfig(saRestConfig)
				require.NoError(t, err)

				t.Run("Access workspace with the service account", func(t *testing.T) {
					_, err := saKubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
				})

				t.Run("Access workspace with the service account, but without /clusters path like InCluster clients", func(t *testing.T) {
					_, err := saKubeClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
					require.NoError(t, err)
				})

				var otherPath logicalcluster.Path
				t.Run("Access another workspace in the same org", func(t *testing.T) {
					t.Log("Create another workspace in the same org")
					otherPath, _ = framework.NewWorkspaceFixture(t, server, orgPath, framework.WithShard(shardB))

					t.Log("Accessing workspace with the service account")
					obj, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
					require.Error(t, err, fmt.Sprintf("expected error accessing workspace with the service account, got: %v", obj))

					t.Log("Giving the access to configmaps in the other workspace")
					_, err = kubeClusterClient.Cluster(otherPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sa-access",
						},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups: []string{""},
								Resources: []string{"configmaps"},
								Verbs:     []string{"get", "list", "watch"},
							},
							{
								Verbs:           []string{"access"},
								NonResourceURLs: []string{"/"},
							},
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create cluster role")
					_, err = kubeClusterClient.Cluster(otherPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{
							Name: "sa-access",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:     "Group",
								APIGroup: "rbac.authorization.k8s.io",
								Name:     "system:authenticated",
							},
						},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							Name:     "sa-access",
							APIGroup: rbacv1.GroupName,
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create cluster role binding")

					t.Log("Accessing other workspace with the (there foreign) service account should eventually work because it is authenticated")
					framework.Eventually(t, func() (bool, string) {
						_, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
						return err == nil, fmt.Sprintf("err = %v", err)
					}, wait.ForeverTestTimeout, time.Millisecond*100)
				})

				t.Run("Access an equally named workspace in another org fails", func(t *testing.T) {
					t.Log("Create namespace with the same name")
					otherOrgPath, _ := framework.NewOrganizationFixture(t, server)
					otherPath, _ := framework.NewWorkspaceFixture(t, server, otherOrgPath, framework.WithShard(shardB))

					t.Log("Accessing workspace with the service account")
					obj, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
					require.Error(t, err, fmt.Sprintf("expected error accessing workspace with the service account, got: %v", obj))
				})

				t.Run("A service account is allowed to escalate permissions implicitly", func(t *testing.T) {
					t.Log("Creating cluster role that allows service account to get secrets and create cluster roles")
					_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{
							Name: fmt.Sprintf("implicit-escalation-%d", i),
						},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups: []string{""},
								Resources: []string{"secrets"},
								Verbs:     []string{"get"},
							},
							{
								APIGroups: []string{"rbac.authorization.k8s.io"},
								Resources: []string{"clusterroles", "clusterrolebindings"},
								Verbs:     []string{"create", "get", "watch", "list", "update", "delete"},
							},
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create role")

					t.Log("Creating cluster role binding")
					_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{
							Name: fmt.Sprintf("implicit-escalation-%d", i),
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "ServiceAccount",
								Name:      "default",
								Namespace: metav1.NamespaceDefault,
							},
						},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							Name:     fmt.Sprintf("implicit-escalation-%d", i),
							APIGroup: rbacv1.GroupName,
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create role")

					t.Log("Verifying if service account is allowed to delegate")
					framework.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
						_, err = saKubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
							ObjectMeta: metav1.ObjectMeta{
								Name: fmt.Sprintf("implicit-escalating-clusterrole-%d", i),
							},
							Rules: []rbacv1.PolicyRule{
								{
									APIGroups: []string{""},
									Resources: []string{"secrets"},
									Verbs:     []string{"get"},
								},
							},
						}, metav1.CreateOptions{})
						if err != nil {
							return false, err.Error()
						}
						return true, ""
					}, wait.ForeverTestTimeout, time.Millisecond*100)
				})

				t.Run("A service account is allowed to escalate permissions explicitly", func(t *testing.T) {
					t.Log("Creating cluster role that allows service account to get secrets and create cluster roles")
					_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{
							Name: fmt.Sprintf("explicit-clusterrole-%d", i),
						},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups: []string{"rbac.authorization.k8s.io"},
								Resources: []string{"clusterroles", "clusterrolebindings"},
								Verbs:     []string{"create", "get", "watch", "list", "update", "delete", "escalate"},
							},
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create role")

					t.Log("Creating cluster role binding")
					_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{
							Name: fmt.Sprintf("explicit-clusterrole-%d", i),
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "ServiceAccount",
								Name:      "default",
								Namespace: metav1.NamespaceDefault,
							},
						},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							Name:     fmt.Sprintf("explicit-clusterrole-%d", i),
							APIGroup: rbacv1.GroupName,
						},
					}, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create role")

					t.Log("Verifying if service account is allowed to escalate")
					framework.Eventually(t, func() (bool, string) { // authz makes this eventually succeed
						_, err = saKubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
							ObjectMeta: metav1.ObjectMeta{
								Name: fmt.Sprintf("explicit-escalating-clusterrole-%d", i),
							},
							Rules: []rbacv1.PolicyRule{
								{
									APIGroups: []string{""},
									Resources: []string{"secrets"},
									Verbs:     []string{"get"},
								},
							},
						}, metav1.CreateOptions{})
						if err != nil {
							return false, err.Error()
						}
						return true, ""
					}, wait.ForeverTestTimeout, time.Millisecond*100)
				})

				t.Run("By revoking, access stops", func(t *testing.T) {
					revokeCase.revoke(t, wsPath, otherPath)

					if revokeCase.revokesLocal {
						t.Log("The service account should not be able to access its workspace anymore eventually")
						framework.Eventually(t, func() (bool, string) {
							_, err := saKubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
							if err == nil {
								return false, "access should not be allowed. Keeping trying."
							}
							return true, ""
						}, wait.ForeverTestTimeout, time.Millisecond*100)
					}

					t.Log("The foreign service account should not be able to access the other workspace anymore eventually")
					framework.Eventually(t, func() (bool, string) {
						_, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{})
						if err == nil {
							return false, "access should not be allowed. Keeping trying."
						}
						return true, ""
					}, wait.ForeverTestTimeout, time.Millisecond*100)
				})
			})
		}
	}
}
