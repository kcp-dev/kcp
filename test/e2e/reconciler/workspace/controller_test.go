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
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace without shards, expect it to be unschedulable",
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
				if err := server.orgExpect(workspace, unschedulable); err != nil {
					t.Errorf("did not see workspace marked unschedulable: %v", err)
					return
				}
			},
		},
		{
			name: "add a shard after a workspace is unschedulable, expect it to be scheduled",
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

				t.Logf("Add a shard")
				bostonShard, err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled")
				err = server.orgExpect(workspace, scheduled(bostonShard.Name))
				require.NoError(t, err, "did not see workspace scheduled")
			},
		},
		{
			name: "create a workspace with a shard, expect it to be scheduled",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create a workspace with a shard")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				err = server.orgExpect(workspace, scheduled("root"))
				require.NoError(t, err, "did not see workspace scheduled")
			},
		},
		{
			name: "delete a shard that a workspace is scheduled to, expect it to move to another shard",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Delete all pre-configured shards, we have to control the creation of the workspace shards in this test")
				err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
				require.NoError(t, err)

				t.Logf("Create two shards: boston and atlanta")
				bostonShard, err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create first workspace shard")
				atlantaShard, err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "atlanta"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create second workspace shard")

				t.Logf("Create a workspace")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to some shard")
				var currentShard, otherShard string
				err = server.orgExpect(workspace, func(current *tenancyv1alpha1.ClusterWorkspace) error {
					expectationErr := scheduledAnywhere(current)
					if expectationErr == nil {
						currentShard = current.Status.Location.Current
						for _, name := range []string{bostonShard.Name, atlantaShard.Name} {
							if name != currentShard {
								otherShard = name
								break
							}
						}
					}
					return expectationErr
				})
				require.NoError(t, err, "did not see workspace scheduled")

				// Only collect the other shard - the current shard won't be available after deletion.
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Get(ctx, otherShard, metav1.GetOptions{})
				})

				t.Logf("Delete the current shard")
				err = server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Delete(ctx, currentShard, metav1.DeleteOptions{})
				require.NoError(t, err, "failed to delete workspace shard")

				t.Logf("Expect workspace to be scheduled to the other shard")
				err = server.orgExpect(workspace, scheduled(otherShard))
				require.NoError(t, err, "did not see workspace rescheduled")
			},
		},
		{
			name: "delete all shards, expect workspace to be unschedulable",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Create workspace with a shard")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to some shard")
				err = server.orgExpect(workspace, scheduled("root"))
				require.NoError(t, err, "did not see workspace scheduled")

				t.Logf("Delete all shards")
				err = server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
				require.NoError(t, err, "failed to delete workspace shard")

				t.Logf("Expect workspace to be unschedulable")
				err = server.orgExpect(workspace, unschedulable)
				require.NoError(t, err, "did not see workspace marked unschedulable")
			},
		},
		{
			name: "create a workspace with a shard that has credentials, expect workspace to have base URL",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Delete all pre-configured shards, we have to control the creation of the workspace shards in this test")
				err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
				require.NoError(t, err)

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

				t.Logf("Create a workspace with a shard that references those credentials")
				workspaceShard, err := server.rootKcpClient.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")

				t.Logf("Expect the shard to show valid credentials")
				err = server.rootExpectShard(workspaceShard, func(shard *tenancyv1alpha1.WorkspaceShard) error {
					if !utilconditions.IsTrue(shard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
						return fmt.Errorf("workspace shard %s does not have valid credentials, conditions: %#v", shard.Name, shard.GetConditions())
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace shard get valid credentials")

				t.Logf("Create a workspace")
				workspace, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to the shard with the base URL stored in the credentials")
				_, orgName, err := helper.ParseLogicalClusterName(workspace.ClusterName)
				require.NoError(t, err, "failed to parse logical cluster name of workspace")
				workspaceClusterName := helper.EncodeOrganizationAndWorkspace(orgName, workspace.Name)
				err = server.orgExpect(workspace, func(workspace *tenancyv1alpha1.ClusterWorkspace) error {
					if err := scheduled(workspaceShard.Name)(workspace); err != nil {
						return err
					}
					if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceURLValid) {
						return fmt.Errorf("expected valid URL on workspace, got: %v", utilconditions.Get(workspace, tenancyv1alpha1.WorkspaceURLValid))
					}
					if diff := cmp.Diff(workspace.Status.BaseURL, "https://kcp.dev/apiprefix/clusters/"+workspaceClusterName); diff != "" {
						return fmt.Errorf("got incorrect base URL on workspace: %v", diff)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace updated")
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			const serverName = "main"
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: serverName,
				},
			)
			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]
			cfg, err := server.Config()
			require.NoError(t, err)

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
		return fmt.Errorf("expected an unschedulable workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	return nil
}

func scheduled(target string) func(workspace *tenancyv1alpha1.ClusterWorkspace) error {
	return func(object *tenancyv1alpha1.ClusterWorkspace) error {
		if isUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Status.Location.Current != target {
			return fmt.Errorf("expected workspace.status.location.current to be %q, got %q", target, object.Status.Location.Current)
		}
		return nil
	}
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
