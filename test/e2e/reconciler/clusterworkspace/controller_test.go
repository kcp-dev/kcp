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
	"strings"
	"testing"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		rootKcpClient, orgKcpClient kcpclientset.Interface
		orgExpect                   framework.RegisterClusterWorkspaceExpectation
		rootExpectShard             framework.RegisterWorkspaceShardExpectation
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

				require.True(t, strings.HasPrefix(cws.Spec.BaseURL, "https://"), "expected https:// root shard base URL, got=%q", cws.Spec.BaseURL)
				require.True(t, strings.HasPrefix(cws.Spec.ExternalURL, "https://"), "expected https:// root shard external URL, got=%q", cws.Spec.ExternalURL)
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
				framework.Eventually(t, func() (bool, string) {
					workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)

					if isUnschedulable(workspace) {
						return false, fmt.Sprintf("unschedulable:\n%s", toYAML(t, workspace))
					}
					if workspace.Status.Location.Current != bostonShard.Name {
						return false, fmt.Sprintf("current location is not %q:\n%s", bostonShard.Name, toYAML(t, workspace))
					}
					if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceShardValid) {
						return false, fmt.Sprintf("shard is not valid:\n%s", toYAML(t, workspace))
					}
					if expected := "https://kcp.dev" + workspaceClusterName.Path(); workspace.Status.BaseURL != expected {
						return false, fmt.Sprintf("baseURL is not %q:\n%s", expected, toYAML(t, workspace))
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100)
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

			cfg := server.BaseConfig(t)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create clients
			orgClusterCfg := kcpclienthelper.ConfigWithCluster(cfg, orgClusterName)
			orgClusterKcpClient, err := kcpclientset.NewForConfig(orgClusterCfg)
			require.NoError(t, err)

			rootClusterCfg := kcpclienthelper.ConfigWithCluster(cfg, tenancyv1alpha1.RootCluster)
			rootClusterKcpClient, err := kcpclientset.NewForConfig(rootClusterCfg)
			require.NoError(t, err)

			orgExpect, err := framework.ExpectClusterWorkspaces(ctx, t, orgClusterKcpClient)
			require.NoError(t, err, "failed to start expecter")

			rootExpectShard, err := framework.ExpectWorkspaceShards(ctx, t, rootClusterKcpClient)
			require.NoError(t, err, "failed to start expecter")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootKcpClient:   rootClusterKcpClient,
				orgKcpClient:    orgClusterKcpClient,
				orgExpect:       orgExpect,
				rootExpectShard: rootExpectShard,
			})
		})
	}
}

func toYAML(t *testing.T, obj interface{}) string {
	bs, err := yaml.Marshal(obj)
	require.NoError(t, err, "failed to marshal object")
	return string(bs)
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
