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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceDeletionController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
		kcpClusterClient  kcpclientset.ClusterInterface
		kubeClusterClient kcpkubernetesclientset.ClusterInterface
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
				workspace, err := server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "ws-cleanup"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.WorkspaceTypeReference{
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
				framework.Eventually(t, func() (bool, string) {
					workspace, err := server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace")

					if len(workspace.Finalizers) == 0 {
						return false, toYAML(t, workspace)
					}

					return conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceScheduled), toYAML(t, workspace)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Wait until the %q workspace is ready", workspace.Name)
				framework.Eventually(t, func() (bool, string) {
					workspace, err := server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace")
					if actual, expected := workspace.Status.Phase, corev1alpha1.LogicalClusterPhaseReady; actual != expected {
						return false, fmt.Sprintf("workspace phase is %s, not %s", actual, expected)
					}
					return workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady, fmt.Sprintf("workspace phase is %s", workspace.Status.Phase)
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for workspace %s to become ready", orgClusterName.Path().Join(workspace.Name))

				workspaceCluster := orgClusterName.Path().Join(workspace.Name)

				t.Logf("Wait for default namespace to be created")
				framework.Eventually(t, func() (bool, string) {
					_, err := server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
					return err == nil, fmt.Sprintf("%v", err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "default namespace was never created")

				t.Logf("Delete default ns should be forbidden")
				err = server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().Namespaces().Delete(ctx, metav1.NamespaceDefault, metav1.DeleteOptions{})
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

				configmap, err = server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().ConfigMaps(metav1.NamespaceDefault).Create(ctx, configmap, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create configmap in workspace %s", workspace.Name)

				t.Logf("Create a namespace in the workspace")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				}
				_, err = server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace %s", workspace.Name)

				err = server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, workspace.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", workspace.Name)

				t.Logf("The workspace condition should be updated since there is resource in the workspace pending finalization.")
				framework.Eventually(t, func() (bool, string) {
					workspace, err := server.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Cluster(orgClusterName.Path()).Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)
					return conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceContentDeleted), toYAML(t, workspace)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Clean finalizer to remove the configmap")
				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					configmap, err = server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().ConfigMaps(metav1.NamespaceDefault).Get(ctx, configmap.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					configmap.Finalizers = []string{}
					_, err := server.kubeClusterClient.Cluster(workspaceCluster).CoreV1().ConfigMaps(metav1.NamespaceDefault).Update(ctx, configmap, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err, "failed to update configmap in workspace %s", workspace.Name)

				t.Logf("Ensure workspace is removed")
				require.Eventually(t, func() bool {
					_, err := server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Finally check if all resources has been removed")

				// Note: we have to access the shard direction to access a logical cluster without workspace
				rootShardKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))

				nslist, err := rootShardKubeClusterClient.Cluster(workspaceCluster).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
				require.NoError(t, err, "failed to list namespaces in workspace %s", workspace.Name)
				require.Equal(t, 0, len(nslist.Items))

				cmlist, err := rootShardKubeClusterClient.Cluster(workspaceCluster).CoreV1().ConfigMaps(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
				require.NoError(t, err, "failed to list configmaps in workspace %s", workspace.Name)
				require.Equal(t, 0, len(cmlist.Items))
			},
		},
		{
			name: "nested worksapce cleanup when an org workspace is deleted",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				org := framework.NewOrganizationFixtureObject(t, server, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
				orgClusterName := logicalcluster.Name(org.Status.Cluster)

				t.Logf("Should have finalizer in org workspace")
				require.Eventually(t, func() bool {
					orgWorkspace, err := server.kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get org workspace %s", org.Name)
					return len(orgWorkspace.Finalizers) > 0
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Create a workspace with in the org workspace")
				ws := framework.NewWorkspaceFixtureObject(t, server.RunningServer, orgClusterName.Path(), framework.WithName("org-ws-cleanup"), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
				wsClusterName := logicalcluster.Name(ws.Status.Cluster)

				t.Logf("Should have finalizer added in workspace")
				require.Eventually(t, func() bool {
					workspace, err := server.kcpClusterClient.Cluster(orgClusterName.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace %s", ws.Name)
					return len(workspace.Finalizers) > 0
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Create namespace %q in both the workspace and org workspace", "deletion-test")
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "deletion-test",
					},
				}

				_, err := server.kubeClusterClient.Cluster(wsClusterName.Path()).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns in workspace root:%s", ws)

				_, err = server.kubeClusterClient.Cluster(orgClusterName.Path()).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create ns %q in workspace root:%s", ns.Name, org.Name)

				// get clients for the right shards. We have to access the shards directly to see object (Namespace and ClusterWorkspace) deletion
				// without being stopped at the (front-proxy) gate because the parent workspace is already gone.
				rootShardKcpClusterClient, err := kcpclientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))
				require.NoError(t, err, "failed to create kcp client for root shard")
				rootShardKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.RunningServer.RootShardSystemMasterBaseConfig(t))
				require.NoError(t, err, "failed to create kube client for root shard")

				t.Logf("Delete org workspace")
				err = server.kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, org.Name, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace %s", org.Name)

				t.Logf("Ensure namespace %q in the workspace is deleted", ns.Name)
				framework.Eventually(t, func() (bool, string) {
					nslist, err := rootShardKubeClusterClient.Cluster(wsClusterName.Path()).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)

					return len(nslist.Items) == 0, toYAML(t, nslist)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Ensure namespace %q in the org workspace is deleted", ns.Name)
				framework.Eventually(t, func() (bool, string) {
					nslist, err := rootShardKubeClusterClient.Cluster(orgClusterName.Path()).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
					require.NoError(t, err)

					return len(nslist.Items) == 0, toYAML(t, nslist)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Ensure workspace in the org workspace is deleted")
				framework.Eventually(t, func() (bool, string) {
					wslist, err := rootShardKcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Cluster(orgClusterName.Path()).List(ctx, metav1.ListOptions{})
					require.NoError(t, err)

					return len(wslist.Items) == 0, toYAML(t, wslist)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Logf("Ensure the org workspace is deleted")
				require.Eventually(t, func() bool {
					_, err := rootShardKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
					return apierrors.IsNotFound(err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)
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

			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")

			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kube client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:     server,
				kcpClusterClient:  kcpClusterClient,
				kubeClusterClient: kubeClusterClient,
			})
		})
	}
}

func toYAML(t *testing.T, obj interface{}) string {
	bs, err := yaml.Marshal(obj)
	require.NoError(t, err, "failed to marshal object")
	return string(bs)
}
