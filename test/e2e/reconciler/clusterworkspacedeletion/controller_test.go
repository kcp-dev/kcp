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

package clusterworkspacedeletion

import (
	"context"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestWorkspaceDeletionController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		orgClusterName    logicalcluster.Name
		orgKcpClient      clientset.Interface
		rootKcpClient     clientset.Interface
		kubeClusterClient *kubernetesclientset.Cluster
	}

	testCases := []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create and clean workspace",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace with a shard")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "ws-cleanup"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")

				t.Logf("Should have finalizer added in workspace")
				require.Eventually(t, func() bool {
					workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}

					if len(workspace.Finalizers) == 0 {
						return false
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				}, wait.ForeverTestTimeout, 1*time.Second)

				workspaceCluster := server.orgClusterName.Join(workspace.Name)
				kubeClient := server.kubeClusterClient.Cluster(workspaceCluster)

				t.Logf("Delete default ns should be forbidden")
				err = kubeClient.CoreV1().Namespaces().Delete(ctx, metav1.NamespaceDefault, metav1.DeleteOptions{})
				if !apierrors.IsForbidden(err) {
					t.Errorf("expect default namespace deletion to be forbidden")
				}

				t.Logf("Create a configmap in the workspace")
				configmap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "test",
						Namespace:  metav1.NamespaceDefault,
						Finalizers: []string{"tenancy.kcp.dev/test-finalizer"},
					},
					Data: map[string]string{
						"foo": "bar",
					},
				}

				configmap, err = kubeClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Create(ctx, configmap, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create configmap in workspace %s", workspace.Name)

				t.Logf("Create a namespace in the workspace")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				}
				_, err = kubeClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", workspace.Name)

				err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, workspace.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

				t.Logf("The workspace condition should be updated since there is resource in the workspace pending finalization.")
				require.Eventually(t, func() bool {
					workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceDeletionContentSuccess) && conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceContentDeleted)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Clean finalizer to remove the configmap")
				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					configmap, err = kubeClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(ctx, configmap.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					configmap.Finalizers = []string{}
					_, err := kubeClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Update(ctx, configmap, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update configmap in workspace %s", workspace.Name)

				t.Logf("Ensure workspace is removed")
				require.Eventually(t, func() bool {
					_, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Finally check if all resources has been removed")
				nslist, err := kubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err, "failed to list namespaces in workspace %s", workspace.Name)
				require.Equal(t, 0, len(nslist.Items))

				cmlist, err := kubeClient.CoreV1().ConfigMaps(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
				require.NoError(t, err, "failed to list configmaps in workspace %s", workspace.Name)
				require.Equal(t, 0, len(cmlist.Items))
			},
		},
		{
			name: "nested worksapce cleanup when an org workspace is deleted",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				var orgWorkspace *tenancyv1alpha1.ClusterWorkspace
				var err error

				t.Logf("Should have finalizer in org workspace")
				orgWorkspaceName := server.orgClusterName.Base()
				require.Eventually(t, func() bool {
					orgWorkspace, err = server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, orgWorkspaceName, metav1.GetOptions{})
					if err != nil {
						return false
					}

					if len(orgWorkspace.Finalizers) == 0 {
						return false
					}

					return conditions.IsTrue(orgWorkspace, tenancyv1alpha1.WorkspaceScheduled)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Create a workspace with in the org workspace")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "org-ws-cleanup"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")

				t.Logf("Should have finalizer added in workspace")
				require.Eventually(t, func() bool {
					workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}

					if len(workspace.Finalizers) == 0 {
						return false
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Create namespace in both the workspace and org workspace")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				}
				workspaceCluster := server.orgClusterName.Join(workspace.Name)
				kubeClient := server.kubeClusterClient.Cluster(workspaceCluster)
				_, err = kubeClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", workspace.Name)

				orgKubeClient := server.kubeClusterClient.Cluster(server.orgClusterName)
				_, err = orgKubeClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", orgWorkspace.Name)

				t.Logf("Delete org workspace")
				err = server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, orgWorkspace.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", orgWorkspace.Name)

				t.Logf("Ensure namespace in the workspace is deleted")
				require.Eventually(t, func() bool {
					nslist, err := kubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
					if err != nil {
						return false
					}

					return len(nslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure namespace in the org workspace is deleted")
				require.Eventually(t, func() bool {
					nslist, err := orgKubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
					if err != nil {
						return false
					}

					return len(nslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure workspace in the org workspace is deleted")
				require.Eventually(t, func() bool {
					wslist, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
					// 404 could be returned if the org workspace is deleted.
					if apierrors.IsNotFound(err) {
						return true
					}

					if err != nil {
						return false
					}

					return len(wslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure the org workspace is deleted")
				require.Eventually(t, func() bool {
					_, err := server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, orgWorkspace.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 1*time.Second)
			},
		},
	}

	sharedServer := framework.SharedKcpServer(t)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			server := sharedServer

			cfg := server.DefaultConfig(t)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			kcpClusterClient, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
			rootKcpClient := kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster)

			kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kube client for server")

			testCase.work(ctx, t, runningServer{
				orgClusterName:    orgClusterName,
				RunningServer:     server,
				orgKcpClient:      orgKcpClient,
				rootKcpClient:     rootKcpClient,
				kubeClusterClient: kubeClusterClient,
			})
		})
	}
}
