/*
Copyright 2021 The KCP Authors.

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

package workspace

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	utilconditions "github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestWorkspaceController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		rootKcpClient, orgKcpClient   clientset.Interface
		rootKubeClient, orgKubeClient kubernetesclientset.Interface
		orgExpect                     framework.RegisterClusterWorkspaceExpectation
		rootExpectShard               framework.RegisterWorkspaceShardExpectation
	}
	var testCases = []struct {
		name        string
		destructive bool
		work        func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace with a shard, expect it to be scheduled",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				// note that the root shard always exists if not deleted

				t.Logf("Create a workspace with a shard")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				err = server.orgExpect(workspace, func(current *tenancyv1alpha1.ClusterWorkspace) error {
					expectationErr := scheduledAnywhere(current)
					return expectationErr
				})
				require.NoError(t, err, "did not see workspace scheduled")
			},
		},
		{
			name:        "add a shard after a workspace is unschedulable, expect it to be scheduled",
			destructive: true,
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Delete all pre-configured shards, we have to control the creation of the workspace shards in this test")
				err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
				require.NoError(t, err)

				t.Logf("Create a workspace without shards")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}, Status: tenancyv1alpha1.ClusterWorkspaceStatus{}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be unschedulable")
				err = server.orgExpect(workspace, unschedulable)
				require.NoError(t, err, "did not see workspace marked unschedulable")

				t.Logf("Create a kubeconfig secret for a workspace shard in the root workspace")
				_, err = server.rootKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials namespace")
				rawCfg := clientcmdapi.Config{
					Clusters:       map[string]*clientcmdapi.Cluster{"cluster": {Server: "https://kcp.dev/apiprefix"}},
					Contexts:       map[string]*clientcmdapi.Context{"context": {Cluster: "cluster", AuthInfo: "user"}},
					CurrentContext: "context",
					AuthInfos:      map[string]*clientcmdapi.AuthInfo{"user": {Username: "user", Password: "password"}},
				}
				rawBytes, err := clientcmd.Write(rawCfg)
				require.NoError(t, err, "could not serialize raw config")
				_, err = server.rootKubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig"},
					Data: map[string][]byte{
						"kubeconfig": rawBytes,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials secret")

				t.Logf("Add a shard, pointing to the credentials/credentials kubeconfig secret")
				bostonShard, err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "boston"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to the shard with the base URL stored in the credentials")
				_, orgName, err := helper.ParseLogicalClusterName(workspace.ClusterName)
				require.NoError(t, err, "failed to parse logical cluster name of workspace")
				workspaceClusterName := helper.EncodeOrganizationAndClusterWorkspace(orgName, workspace.Name)
				err = server.orgExpect(workspace, func(workspace *tenancyv1alpha1.ClusterWorkspace) error {
					if err := scheduled(bostonShard.Name)(workspace); err != nil {
						return err
					}
					if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceShardValid) {
						return fmt.Errorf("expected valid URL on workspace, got: %v", utilconditions.Get(workspace, tenancyv1alpha1.WorkspaceShardValid))
					}
					if diff := cmp.Diff(workspace.Status.BaseURL, "https://kcp.dev/apiprefix/clusters/"+workspaceClusterName); diff != "" {
						return fmt.Errorf("got incorrect base URL on workspace: %v", diff)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace updated")
			},
		},
		{
			name:        "delete a shard that a workspace is scheduled to, expect WorkspaceShardValid condition to turn false",
			destructive: true,
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to some shard")
				var currentShard string
				err = server.orgExpect(workspace, func(current *tenancyv1alpha1.ClusterWorkspace) error {
					expectationErr := scheduledAnywhere(current)
					if expectationErr == nil {
						currentShard = current.Status.Location.Current
					}
					return expectationErr
				})
				require.NoError(t, err, "did not see workspace scheduled")

				t.Logf("Delete the current shard")
				err = server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Delete(ctx, currentShard, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace shard")

				t.Logf("Expect WorkspaceShardValid condition to turn false")
				err = server.orgExpect(workspace, invalidShard)
				require.NoError(t, err, "did not see WorkspaceShardValid condition to turn false")
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
			if testCase.destructive {
				// Destructive tests require their own server
				//
				// TODO(marun) Could the testing currently requiring destructive e2e be performed with less cost?
				const serverName = "main"
				f := framework.NewKcpFixture(t, framework.KcpConfig{Name: serverName})
				server = f.Servers[serverName]
			}

			cfg := server.DefaultConfig(t)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create clients
			kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
			rootKcpClient := kcpClusterClient.Cluster(helper.RootCluster)

			orgExpect, err := framework.ExpectClusterWorkspaces(ctx, t, orgKcpClient)
			require.NoError(t, err, "failed to start expecter")

			rootExpectShard, err := framework.ExpectWorkspaceShards(ctx, t, rootKcpClient)
			require.NoError(t, err, "failed to start expecter")

			kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kube client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootKcpClient:   rootKcpClient,
				orgKcpClient:    orgKcpClient,
				rootKubeClient:  kubeClusterClient.Cluster(helper.RootCluster),
				orgKubeClient:   kubeClusterClient.Cluster(orgClusterName),
				orgExpect:       orgExpect,
				rootExpectShard: rootExpectShard,
			})
		})
	}
}

func isUnschedulable(workspace *tenancyv1alpha1.ClusterWorkspace) bool {
	return utilconditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && utilconditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable
}

func unschedulable(object *tenancyv1alpha1.ClusterWorkspace) error {
	if !isUnschedulable(object) {
		klog.Infof("Workspace is not unscheduled: %v", utilconditions.Get(object, tenancyv1alpha1.WorkspaceScheduled))
		return fmt.Errorf("expected an unschedulable workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	return nil
}

func scheduled(target string) func(workspace *tenancyv1alpha1.ClusterWorkspace) error {
	return func(object *tenancyv1alpha1.ClusterWorkspace) error {
		if isUnschedulable(object) {
			klog.Infof("Workspace %s is unschedulable", object.Name)
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Status.Location.Current != target {
			klog.Infof("Workspace %s is scheduled to %s, expected %s", object.Name, object.Status.Location.Current, target)
			return fmt.Errorf("expected workspace.status.location.current to be %q, got %q", target, object.Status.Location.Current)
		}
		return nil
	}
}

func invalidShard(workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if !utilconditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceShardValid) {
		return fmt.Errorf("expected invalid shard, got status.conditions: %#v", workspace.Status.Conditions)
	}
	return nil
}

func scheduledAnywhere(object *tenancyv1alpha1.ClusterWorkspace) error {
	if isUnschedulable(object) {
		return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	if object.Status.Location.Current == "" {
		return fmt.Errorf("expected workspace.status.location.current to be anything, got %q", object.Status.Location.Current)
	}
	return nil
}
