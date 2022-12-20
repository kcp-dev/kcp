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

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	type runningServer struct {
		framework.RunningServer
		orgClusterName    logicalcluster.Path
		kubeClusterClient kcpkubernetesclientset.ClusterInterface
		kcpClusterClient  kcpclientset.ClusterInterface
		UserKcpClients    []kcpclientset.ClusterInterface
	}

	var testCases = []struct {
		name       string
		userTokens []string
		work       func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name:       "create a workspace in an org as org content admin, and have only its creator list it, not another user with just access",
			userTokens: []string{"user-1-token", "user-2-token", "user-3-token"},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				user1Client := server.UserKcpClients[0]
				user2Client := server.UserKcpClients[1]
				user3Client := server.UserKcpClients[2]

				permitAccessToWorkspace(t, ctx, server.kubeClusterClient, server.orgClusterName, true, "team-1-access", "team-1")
				permitAccessToWorkspace(t, ctx, server.kubeClusterClient, server.orgClusterName, true, "team-2-access", "team-2")
				permitAccessToWorkspace(t, ctx, server.kubeClusterClient, server.orgClusterName, false, "team-3-access", "team-3")

				t.Logf("Create Workspace workspace1 in the virtual workspace")
				var workspace1 *tenancyv1beta1.Workspace
				require.Eventually(t, func() bool {
					// RBAC authz uses informers and needs a moment to understand the new roles. Hence, try until successful.
					var err error
					workspace1, err = user1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
						ObjectMeta: metav1.ObjectMeta{Name: "workspace1"},
					}, metav1.CreateOptions{})
					if err != nil {
						t.Logf("Failed to create workspace1: %v", err)
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create workspace1")

				t.Logf("Workspace will show up in list of user1")
				list, err := user1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
				require.Equal(t, workspace1.Name, list.Items[0].Name)

				t.Logf("Workspace will show up in list of user2")
				list, err = user2Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
				require.Equal(t, workspace1.Name, list.Items[0].Name)

				t.Logf("Workspace will show up in list of user3")
				list, err = user3Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Len(t, list.Items, 1)
				require.Equal(t, workspace1.Name, list.Items[0].Name)

				t.Logf("Workspace will become ready")
				framework.Eventually(t, func() (bool, string) {
					workspace1, err = user1Client.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
					require.NoError(t, err)
					return workspace1.Status.Phase != corev1alpha1.LogicalClusterPhaseReady, fmt.Sprintf("workspace1 phase: %s", workspace1.Status.Phase)
				}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace1 never became ready")

				t.Logf("User1 is admin of workspace1 and can list and create sub-workspaces")
				framework.Eventually(t, func() (bool, string) {
					// Bindings are async. better wait.
					_, err = user1Client.Cluster(server.orgClusterName.Join("workspace1")).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
					return err == nil, fmt.Sprintf("list of sub-workspaces: %v", err)
				}, wait.ForeverTestTimeout, time.Millisecond*100, "user1 failed to list sub-workspaces")
				_, err = user1Client.Cluster(server.orgClusterName.Join("workspace1")).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: "nested"},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				t.Logf("User2 cannot access workspace1")
				_, err = user2Client.Cluster(server.orgClusterName.Join("workspace1")).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.Error(t, err)

				t.Logf("User3 cannot access workspace1")
				_, err = user3Client.Cluster(server.orgClusterName.Join("workspace1")).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.Error(t, err)

				t.Logf("User2 can be given access to workspace1")
				permitAccessToWorkspace(t, ctx, server.kubeClusterClient, server.orgClusterName.Join("workspace1"), false, "team-2-access", "team-2", "workspace1")
				framework.Eventually(t, func() (bool, string) {
					// Bindings are async. better wait.
					_, err = user2Client.Cluster(server.orgClusterName.Join("workspace1")).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
					return err == nil, fmt.Sprintf("list of sub-workspaces: %v", err)
				}, wait.ForeverTestTimeout, time.Millisecond*100, "user2 failed to list sub-workspaces")
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create clients
			kcpConfig := server.BaseConfig(t)
			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			// create virtual clients for all paths and users requested
			var userKcpClients []kcpclientset.ClusterInterface
			for _, token := range testCase.userTokens {
				userKcpClient, err := kcpclientset.NewForConfig(framework.ConfigWithToken(token, rest.CopyConfig(kcpConfig)))
				require.NoError(t, err, "failed to construct client for server")
				userKcpClients = append(userKcpClients, userKcpClient)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:     server,
				orgClusterName:    orgClusterName.Path(),
				kubeClusterClient: kubeClusterClient,
				kcpClusterClient:  kcpClusterClient,
				UserKcpClients:    userKcpClients,
			})
		})
	}
}

func permitAccessToWorkspace(t *testing.T, ctx context.Context, kubeClusterClient kcpkubernetesclientset.ClusterInterface, clusterName logicalcluster.Path, admin bool, bindingName string, groupNames ...string) {
	t.Logf("Giving groups %v member access to workspace %q", groupNames, clusterName)

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: bindingName,
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     bindingName,
		},
	}
	for _, groupName := range groupNames {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{
			Kind:      "Group",
			Name:      groupName,
			Namespace: "",
		})
	}
	if admin {
		binding.RoleRef.Name = "cluster-admin"
	} else {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: bindingName,
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:           []string{"access"},
					NonResourceURLs: []string{"/"},
				},
				{
					Verbs:     []string{"get", "list", "watch"},
					APIGroups: []string{"tenancy.kcp.dev"},
					Resources: []string{"workspaces"},
				},
			},
		}
		_, err := kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	_, err := kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
}
