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
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceDeletionController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		kcpClusterClient  clientset.Interface
		kubeClusterClient kubernetesclientset.Interface
	}

	testCases := []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create and clean workspace",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				orgClusterName := framework.NewOrganizationFixture(t, server)

				t.Logf("Create a workspace with a shard")
				workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Create(logicalcluster.WithCluster(ctx, orgClusterName), &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "ws-cleanup"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "universal",
							Path: "root",
						},
						Shard: &tenancyv1alpha1.ShardConstraints{
							Name: "root",
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")

				t.Logf("Should have finalizer added in workspace")
				require.Eventually(t, func() bool {
					workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, orgClusterName), workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}

					if len(workspace.Finalizers) == 0 {
						return false
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				}, wait.ForeverTestTimeout, 1*time.Second)

				workspaceCluster := orgClusterName.Join(workspace.Name)

				t.Logf("Wait for default namespace to be created")
				framework.Eventually(t, func() (bool, string) {
					_, err := server.kubeClusterClient.CoreV1().Namespaces().Get(logicalcluster.WithCluster(ctx, workspaceCluster), "default", metav1.GetOptions{})
					return err == nil, fmt.Sprintf("%v", err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "default namespace was never created")

				t.Logf("Delete default ns should be forbidden")
				err = server.kubeClusterClient.CoreV1().Namespaces().Delete(logicalcluster.WithCluster(ctx, workspaceCluster), metav1.NamespaceDefault, metav1.DeleteOptions{})
				if !apierrors.IsForbidden(err) {
					t.Fatalf("expect default namespace deletion to be forbidden")
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

				configmap, err = server.kubeClusterClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Create(logicalcluster.WithCluster(ctx, workspaceCluster), configmap, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create configmap in workspace %s", workspace.Name)

				t.Logf("Create a namespace in the workspace")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				}
				_, err = server.kubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, workspaceCluster), ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", workspace.Name)

				err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Delete(logicalcluster.WithCluster(ctx, orgClusterName), workspace.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

				t.Logf("The workspace condition should be updated since there is resource in the workspace pending finalization.")
				require.Eventually(t, func() bool {
					workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, orgClusterName), workspace.Name, metav1.GetOptions{})
					if err != nil {
						return false
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceDeletionContentSuccess) && conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceContentDeleted)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Clean finalizer to remove the configmap")
				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					configmap, err = server.kubeClusterClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(logicalcluster.WithCluster(ctx, workspaceCluster), configmap.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					configmap.Finalizers = []string{}
					_, err := server.kubeClusterClient.CoreV1().ConfigMaps(metav1.NamespaceDefault).Update(logicalcluster.WithCluster(ctx, workspaceCluster), configmap, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update configmap in workspace %s", workspace.Name)

				t.Logf("Ensure workspace is removed")
				require.Eventually(t, func() bool {
					_, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, orgClusterName), workspace.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Finally check if all resources has been removed")

				// Note: we have to access the shard direction to access a logical cluster without workspace
				rootShardKubeClusterClient, err := kubernetesclientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))

				nslist, err := rootShardKubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, workspaceCluster), metav1.ListOptions{})
				require.NoError(t, err, "failed to list namespaces in workspace %s", workspace.Name)
				require.Equal(t, 0, len(nslist.Items))

				cmlist, err := rootShardKubeClusterClient.CoreV1().ConfigMaps(metav1.NamespaceAll).List(logicalcluster.WithCluster(ctx, workspaceCluster), metav1.ListOptions{})
				require.NoError(t, err, "failed to list configmaps in workspace %s", workspace.Name)
				require.Equal(t, 0, len(cmlist.Items))
			},
		},
		{
			name: "nested worksapce cleanup when an org workspace is deleted",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				orgClusterName := framework.NewOrganizationFixture(t, server, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))

				t.Logf("Should have finalizer in org workspace")
				orgWorkspaceName := orgClusterName.Base()
				require.Eventually(t, func() bool {
					orgWorkspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, tenancyv1alpha1.RootCluster), orgWorkspaceName, metav1.GetOptions{})
					require.NoError(t, err, "failed to get org workspace %s", orgWorkspaceName)
					return len(orgWorkspace.Finalizers) > 0
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Create a workspace with in the org workspace")
				workspaceClusterName := framework.NewWorkspaceFixture(t, server.RunningServer, orgClusterName, framework.WithName("org-ws-cleanup"), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
				workspaceName := workspaceClusterName.Base()

				t.Logf("Should have finalizer added in workspace")
				require.Eventually(t, func() bool {
					workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, orgClusterName), workspaceName, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace %s", workspaceName)
					return len(workspace.Finalizers) > 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Create namespace in both the workspace and org workspace")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				}

				_, err := server.kubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, workspaceClusterName), ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", workspaceClusterName)

				_, err = server.kubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, orgClusterName), ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", orgClusterName)

				// get clients for the right shards. We have to access the shards directly to see object (Namespace and ClusterWorkspace) deletion
				// without being stopped at the (front-proxy) gate because the parent workspace is already gone.
				rootShardKcpClusterClient, err := clientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))
				require.NoError(t, err, "failed to create kcp client for root shard")
				rootShardKubeClusterClient, err := kubernetesclientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))
				require.NoError(t, err, "failed to create kube client for root shard")

				t.Logf("Delete org workspace")
				err = server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Delete(logicalcluster.WithCluster(ctx, tenancyv1alpha1.RootCluster), orgWorkspaceName, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", orgWorkspaceName)

				t.Logf("Ensure namespace in the workspace is deleted")
				require.Eventually(t, func() bool {
					nslist, err := rootShardKubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, workspaceClusterName), metav1.ListOptions{})
					if err != nil {
						return false
					}

					return len(nslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure namespace in the org workspace is deleted")
				require.Eventually(t, func() bool {
					nslist, err := rootShardKubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, orgClusterName), metav1.ListOptions{})
					if err != nil {
						return false
					}
					return len(nslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure workspace in the org workspace is deleted")
				require.Eventually(t, func() bool {
					wslist, err := rootShardKcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().List(logicalcluster.WithCluster(ctx, orgClusterName), metav1.ListOptions{})
					// 404 could be returned if the org workspace is deleted.
					if apierrors.IsNotFound(err) {
						return true
					}
					require.NoError(t, err, "failed to list workspaces in org workspace")
					return len(wslist.Items) == 0
				}, wait.ForeverTestTimeout, 1*time.Second)

				t.Logf("Ensure the org workspace is deleted")
				require.Eventually(t, func() bool {
					_, err := rootShardKcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, tenancyv1alpha1.RootCluster), orgWorkspaceName, metav1.GetOptions{})
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

			cfg := server.BaseConfig(t)

			kcpClusterClient, err := clientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			kubeClusterClient, err := kubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kube client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:     server,
				kcpClusterClient:  kcpClusterClient,
				kubeClusterClient: kubeClusterClient,
			})
		})
	}
}
