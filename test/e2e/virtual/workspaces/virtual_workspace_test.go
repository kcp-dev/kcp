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
	"errors"
	"fmt"
	"testing"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	utilconditions "github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		client clientset.Interface
		expect framework.RegisterWorkspaceExpectation
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
				defer server.Artifact(t, func() (runtime.Object, error) {
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
				defer server.Artifact(t, func() (runtime.Object, error) {
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
				defer server.Artifact(t, func() (runtime.Object, error) {
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
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				defer server.Artifact(t, func() (runtime.Object, error) {
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
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, bostonShard.Name, metav1.GetOptions{})
				})
				atlantaShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "atlanta"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create second workspace shard: %v", err)
					return
				}
				defer server.Artifact(t, func() (runtime.Object, error) {
					return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, atlantaShard.Name, metav1.GetOptions{})
				})
				workspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				if err != nil {
					t.Errorf("failed to create workspace: %v", err)
					return
				}
				defer server.Artifact(t, func() (runtime.Object, error) {
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
				defer server.Artifact(t, func() (runtime.Object, error) {
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
	}
	const serverName = "main"
	for i := range testCases {
		testCase := testCases[i]
		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer) {
			ctx := context.Background()
			/*
				secureOptions := kubeoptions.NewSecureServingOptions().WithLoopback()
				secureOptions.BindPort, _ =  framework.GetFreePort(t)
				vwOptions := virtualcmd.APIServerOptions{
					Output: os.Stdout,
					SecureServing: options.SecureServingOptionsWithLoopback,
					SubCommandOptions: &workspacescmd.WorkspacesSubCommandOptions{
						KubeconfigFile: servers[serverName].KubeconfigPath(),
					},
				}
				go func() { _ = workspacesCmd.Run(ctx) }()
			*/
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
			clusterName, err := detectClusterName(cfg, ctx)
			if err != nil {
				t.Errorf("failed to detect cluster name: %v", err)
				return
			}
			clients, err := clientset.NewClusterForConfig(cfg)
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
			testCase.work(ctx, t, runningServer{
				RunningServer: server,
				client:        client,
				expect:        expect,
			})
		}, framework.KcpConfig{
			Name: serverName,
			Args: []string{"--install_workspace_controller"},
		})
	}
}

// TODO: we need to undo the prefixing and get normal sharding behavior in soon ... ?
func detectClusterName(cfg *rest.Config, ctx context.Context) (string, error) {
	crdClient, err := apiextensionsclientset.NewClusterForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to construct client for server: %w", err)
	}
	crds, err := crdClient.Cluster("*").ApiextensionsV1().CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list crds: %w", err)
	}
	if len(crds.Items) == 0 {
		return "", errors.New("found no crds, cannot detect cluster name")
	}
	for _, crd := range crds.Items {
		if crd.ObjectMeta.Name == "workspaces.tenancy.kcp.dev" {
			return crd.ObjectMeta.ClusterName, nil
		}
	}
	return "", errors.New("detected no admin cluster")
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
