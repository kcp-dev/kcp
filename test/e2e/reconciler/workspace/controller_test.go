//go:build e2e
// +build e2e

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	utilconditions "github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestWorkspaceController(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client      clientset.Interface
		kubeClient  kubernetesclientset.Interface
		expect      framework.RegisterWorkspaceExpectation
		expectShard framework.RegisterWorkspaceShardExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, server runningServer)
	}{
		{
			name: "create a workspace without shards, expect it to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}, Status: tenancyv1alpha1.WorkspaceStatus{}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, unschedulable); err != nil {
					t.Errorf("did not see workspace marked unschedulable: %v", err)
					return
				}
			},
		},
		{
			name: "add a shard after a workspace is unschedulable, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, unschedulable); err != nil {
					t.Errorf("did not see workspace marked unschedulable: %v", err)
					return
				}
				bostonShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace shard: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, scheduled(bostonShard.Name)); err != nil {
					t.Errorf("did not see workspace scheduled: %v", err)
					return
				}
			},
		},
		{
			name: "create a workspace with a shard, expect it to be scheduled",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				bostonShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, scheduled(bostonShard.Name)); err != nil {
					t.Errorf("did not see workspace scheduled: %v", err)
					return
				}
			},
		},
		{
			name: "delete a shard that a workspace is scheduled to, expect it to move to another shard",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				bostonShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})
				atlantaShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "atlanta"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create second workspace shard: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, atlantaShard.Name, metav1.GetOptions{})
				})
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				var currentShard, otherShard string
				if err := server.expect(workspace, func(current *tenancyv1alpha1.Workspace) error {
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
				}); err != nil {
					t.Errorf("did not see workspace scheduled: %v", err)
					return
				}

				err = server.client.TenancyV1alpha1().WorkspaceShards().Delete(ctx, currentShard, metav1.DeleteOptions{})
				if err != nil {
					t.Errorf("failed to delete workspace shard: %v", err)
					return
				}
				if err := server.expect(workspace, scheduled(otherShard)); err != nil {
					t.Errorf("did not see workspace rescheduled: %v", err)
					return
				}
			},
		},
		{
			name: "delete all shards, expect workspace to be unschedulable",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				bostonShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "boston"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create first workspace shard: %v", err)
					return
				}
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, scheduled(bostonShard.Name)); err != nil {
					t.Errorf("did not see workspace scheduled: %v", err)
					return
				}
				err = server.client.TenancyV1alpha1().WorkspaceShards().Delete(ctx, bostonShard.Name, metav1.DeleteOptions{})
				if err != nil {
					t.Errorf("failed to delete workspace shard: %v", err)
					return
				}
				if err := server.expect(workspace, unschedulable); err != nil {
					t.Errorf("did not see workspace marked unschedulable: %v", err)
					return
				}
			},
		},
		{
			name: "create a workspace with a shard that has credentials, expect workspace to have base URL",
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				if _, err := server.kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{}); err != nil {
					t.Errorf("failed to create credentials namespace: %v", err)
					return
				}
				rawCfg := clientcmdapi.Config{
					Clusters:       map[string]*clientcmdapi.Cluster{"cluster": {Server: "https://kcp.dev/apiprefix"}},
					Contexts:       map[string]*clientcmdapi.Context{"context": {Cluster: "cluster", AuthInfo: "user"}},
					CurrentContext: "context",
					AuthInfos:      map[string]*clientcmdapi.AuthInfo{"user": {Username: "user", Password: "password"}},
				}
				rawBytes, err := clientcmd.Write(rawCfg)
				if err != nil {
					t.Errorf("could not serialize raw config: %v", err)
					return
				}
				if _, err := server.kubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig"},
					Data: map[string][]byte{
						"kubeconfig": rawBytes,
					},
				}, metav1.CreateOptions{}); err != nil {
					t.Errorf("failed to create credentials secret: %v", err)
					return
				}
				workspaceShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace shard: %v", err)
					return
				}
				if err := server.expectShard(workspaceShard, func(shard *tenancyv1alpha1.WorkspaceShard) error {
					if !utilconditions.IsTrue(shard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
						return fmt.Errorf("workspace shard %s does not have valid credentials, conditions: %#v", shard.Name, shard.GetConditions())
					}
					return nil
				}); err != nil {
					t.Errorf("did not see workspace shard get valid credentials: %v", err)
					return
				}
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})
				if err := server.expect(workspace, func(workspace *tenancyv1alpha1.Workspace) error {
					if err := scheduled(workspaceShard.Name)(workspace); err != nil {
						return err
					}
					if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceURLValid) {
						return fmt.Errorf("expected valid URL on workspace, got: %v", utilconditions.Get(workspace, tenancyv1alpha1.WorkspaceURLValid))
					}
					if diff := cmp.Diff(workspace.Status.BaseURL, "https://kcp.dev/apiprefix/clusters/admin_steve"); diff != "" {
						return fmt.Errorf("got incorrect base URL on workspace: %v", diff)
					}
					return nil
				}); err != nil {
					t.Errorf("did not see workspace updated: %v", err)
					return
				}
			},
		},
	}
	const serverName = "main"
	for i := range testCases {
		testCase := testCases[i]
		framework.RunParallel(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}
			if len(servers) != 1 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			server := servers[serverName]
			cfg, err := server.Config()
			if err != nil {
				t.Error(err)
				return
			}
			clusterName, err := framework.DetectClusterName(cfg, ctx, "workspaces.tenancy.kcp.dev")
			if err != nil {
				t.Errorf("failed to detect cluster name: %v", err)
				return
			}
			clients, err := kcpclientset.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct client for server: %v", err)
				return
			}
			client := clients.Cluster(clusterName)
			expect, err := framework.ExpectWorkspaces(ctx, t, client)
			if err != nil {
				t.Errorf("failed to start expecter: %v", err)
				return
			}
			expectShard, err := framework.ExpectWorkspaceShards(ctx, t, client)
			if err != nil {
				t.Errorf("failed to start expecter: %v", err)
				return
			}
			kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct kube client for server: %v", err)
				return
			}
			kubeClient := kubeClients.Cluster(clusterName)
			testCase.work(ctx, t, runningServer{
				RunningServer: server,
				client:        client,
				kubeClient:    kubeClient,
				expect:        expect,
				expectShard:   expectShard,
			})
		}, framework.KcpConfig{
			Name: serverName,
			Args: []string{"--install-workspace-scheduler"},
		})
	}
}

func isUnschedulable(workspace *tenancyv1alpha1.Workspace) bool {
	return utilconditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && utilconditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable
}

func unschedulable(object *tenancyv1alpha1.Workspace) error {
	if !isUnschedulable(object) {
		return fmt.Errorf("expected an unschedulable workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	return nil
}

func scheduled(target string) func(workspace *tenancyv1alpha1.Workspace) error {
	return func(object *tenancyv1alpha1.Workspace) error {
		if isUnschedulable(object) {
			return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
		}
		if object.Status.Location.Current != target {
			return fmt.Errorf("expected workspace.status.location.current to be %q, got %q", target, object.Status.Location.Current)
		}
		return nil
	}
}

func scheduledAnywhere(object *tenancyv1alpha1.Workspace) error {
	if isUnschedulable(object) {
		return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	if object.Status.Location.Current == "" {
		return fmt.Errorf("expected workspace.status.location.current to be anything, got %q", object.Status.Location.Current)
	}
	return nil
}
