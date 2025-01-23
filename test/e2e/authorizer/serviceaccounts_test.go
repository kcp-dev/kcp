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
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1/helper"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestServiceAccounts(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	wsPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Creating namespace")
	namespace, err := kubeClusterClient.Cluster(wsPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-sa-",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create namespace")

	t.Log("Creating role to access configmaps")
	_, err = kubeClusterClient.Cluster(wsPath).RbacV1().Roles(namespace.Name).Create(ctx, &rbacv1.Role{
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
	_, err = kubeClusterClient.Cluster(wsPath).RbacV1().RoleBindings(namespace.Name).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sa-access-configmap",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "default",
				Namespace: namespace.Name,
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
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(namespace.Name).Get(ctx, "default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Fatalf("unexpected error retrieving service account: %v", err)
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default\" service account not created in namespace %s",
		helper.QualifiedObjectName(namespace),
	)

	t.Log("Creating the service account secret manually")
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(namespace.Name).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default-token",
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: "default",
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create service account secret")

	t.Log("Waiting for service account secret to be filled")
	var tokenSecret corev1.Secret
	require.Eventually(t, func() bool {
		s, err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets(namespace.Name).Get(ctx, "default-token", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Fatalf("unexpected error retrieving service account secret: %v", err)
		}
		tokenSecret = *s
		return len(s.Data) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default-token\" secret not filled in namespace %s")

	testCases := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{"Legacy token", func(t *testing.T) string {
			t.Helper()
			return string(tokenSecret.Data["token"])
		}},
		{"Bound service token", func(t *testing.T) string {
			t.Helper()
			t.Log("Creating service account bound token")
			boundToken, err := kubeClusterClient.Cluster(wsPath).CoreV1().ServiceAccounts(namespace.Name).CreateToken(ctx, "default", &authenticationv1.TokenRequest{
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
	for i, ttc := range testCases {
		t.Run(ttc.name, func(t *testing.T) {
			t.Parallel()

			saRestConfig := framework.ConfigWithToken(ttc.token(t), server.BaseConfig(t))
			saKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(saRestConfig)
			require.NoError(t, err)
			saKubeClient, err := kubernetes.NewForConfig(saRestConfig)
			require.NoError(t, err)

			t.Run("Access workspace with the service account", func(t *testing.T) {
				_, err := saKubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
			})

			t.Run("Access workspace with the service account, but without /clusters path like InCluster clients", func(t *testing.T) {
				_, err := saKubeClient.CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
			})

			t.Run("Access another workspace in the same org", func(t *testing.T) {
				t.Log("Create namespace with the same name ")
				otherPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
				_, err := kubeClusterClient.Cluster(otherPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace.Name,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace in other workspace")

				t.Log("Accessing workspace with the service account")
				obj, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
				require.Error(t, err, fmt.Sprintf("expected error accessing workspace with the service account, got: %v", obj))
			})

			t.Run("Access an equally named workspace in another org", func(t *testing.T) {
				t.Log("Create namespace with the same name")
				otherOrgPath, _ := framework.NewOrganizationFixture(t, server)
				otherPath, _ := framework.NewWorkspaceFixture(t, server, otherOrgPath)
				_, err := kubeClusterClient.Cluster(otherPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace.Name,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace in other workspace")

				t.Log("Accessing workspace with the service account")
				obj, err := saKubeClusterClient.Cluster(otherPath).CoreV1().ConfigMaps(namespace.Name).List(ctx, metav1.ListOptions{})
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
							Namespace: namespace.Name,
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
							Namespace: namespace.Name,
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
		})
	}
}
