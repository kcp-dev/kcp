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

package clusterworkspace

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
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
			name: "check the root workspace shard has the correct base URL",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				cws, err := server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, "root", metav1.GetOptions{})
				require.NoError(t, err, "did not see root workspace shard")

				err = server.rootExpectShard(cws, func(current *tenancyv1alpha1.ClusterWorkspaceShard) error {
					if u, err := url.Parse(cws.Spec.BaseURL); err != nil {
						return err
					} else if cfg := server.RunningServer.DefaultConfig(t); u.Scheme+"://"+u.Host != cfg.Host {
						return fmt.Errorf("expected ClusterWorkspaceShard baseURL to equal %q, got %q", cfg.Host, u.Host)
					}
					return nil
				})
				require.NoError(t, err, "did not see the root workspace shard updated with the expected base URL")
			},
		},
		{
			name: "create a workspace with the default shard, expect it to be scheduled",
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
				err := server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
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

				t.Logf("Add a shard with custom external address")
				bostonShard, err := server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "boston"},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://boston.kcp.dev",
						ExternalURL: "https://kcp.dev",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to the shard and show the external URL")
				parentName := logicalcluster.From(workspace)
				workspaceClusterName := parentName.Join(workspace.Name)
				err = server.orgExpect(workspace, func(workspace *tenancyv1alpha1.ClusterWorkspace) error {
					if err := scheduled(bostonShard.Name)(workspace); err != nil {
						return err
					}
					if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceShardValid) {
						return fmt.Errorf("expected valid URL on workspace, got: %v", utilconditions.Get(workspace, tenancyv1alpha1.WorkspaceShardValid))
					}
					if diff := cmp.Diff(workspace.Status.BaseURL, "https://kcp.dev"+workspaceClusterName.Path()); diff != "" {
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
				err = server.rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards().Delete(ctx, currentShard, metav1.DeleteOptions{})
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
				server = framework.PrivateKcpServer(t)
			}

			cfg := server.DefaultConfig(t)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create clients
			kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct client for server")
			orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
			rootKcpClient := kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster)

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
				rootKubeClient:  kubeClusterClient.Cluster(tenancyv1alpha1.RootCluster),
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
			klog.Infof("Workspace %s|%s is unschedulable", logicalcluster.From(object), object.Name)
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Status.Location.Current != target {
			klog.Infof("Workspace %s|%s is scheduled to %s, expected %s", logicalcluster.From(object), object.Name, object.Status.Location.Current, target)
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
