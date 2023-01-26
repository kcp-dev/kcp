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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
		rootWorkspaceKcpClient, orgWorkspaceKcpClient kcpclientset.Interface
		orgExpect                                     framework.RegisterWorkspaceExpectation
		rootExpectShard                               framework.RegisterWorkspaceShardExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "check the root workspace shard has the correct base URL",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				wShard, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, "root", metav1.GetOptions{})
				require.NoError(t, err, "did not see root workspace shard")

				require.True(t, strings.HasPrefix(wShard.Spec.BaseURL, "https://"), "expected https:// root shard base URL, got=%q", wShard.Spec.BaseURL)
				require.True(t, strings.HasPrefix(wShard.Spec.ExternalURL, "https://"), "expected https:// root shard external URL, got=%q", wShard.Spec.ExternalURL)
			},
		},
		{
			name: "create a workspace with the default shard, expect it to be scheduled",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()
				// note that the root shard always exists if not deleted

				t.Logf("Create a workspace with a shard")
				workspace, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				err = server.orgExpect(workspace, func(current *tenancyv1alpha1.Workspace) error {
					expectationErr := scheduledAnywhere(current)
					return expectationErr
				})
				require.NoError(t, err, "did not see workspace scheduled")
			},
		},
		{
			name: "add a shard after a workspace is unschedulable, expect it to be scheduled",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				t.Logf("Get the root shard so we can use its URLs")
				rootShard, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, "root", metav1.GetOptions{})
				require.NoError(t, err)

				random := uuid.NewString()
				t.Logf("Create a workspace with a label selector that matches nothing for now (%s)", random)
				workspace := &tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{Name: "steve"},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Location: &tenancyv1alpha1.WorkspaceLocation{
							Selector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"match": random},
							},
						},
					},
				}
				_, err = server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, workspace, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be unschedulable")
				err = server.orgExpect(workspace, unschedulable)
				require.NoError(t, err, "did not see workspace marked unschedulable")

				t.Logf("Add a shard with matching labels")
				newShard, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Create(ctx, &corev1alpha1.Shard{
					ObjectMeta: metav1.ObjectMeta{
						GenerateName: "e2e-matching-shard-",
						Labels:       map[string]string{"match": random},
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:     rootShard.Spec.BaseURL,
						ExternalURL: rootShard.Spec.ExternalURL,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create shard")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, newShard.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to the shard and show the external URL")
				framework.Eventually(t, func() (bool, string) {
					workspace, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
					require.NoError(t, err)

					if isUnschedulable(workspace) {
						return false, fmt.Sprintf("unschedulable:\n%s", toYAML(t, workspace))
					}
					if workspace.Spec.Cluster == "" {
						return false, fmt.Sprintf("spec.cluster is empty\n%s", toYAML(t, workspace))
					}
					if expected := rootShard.Spec.ExternalURL + "/clusters/" + workspace.Spec.Cluster; workspace.Spec.URL != expected {
						return false, fmt.Sprintf("URL is not %q:\n%s", expected, toYAML(t, workspace))
					}
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100)
				require.NoError(t, err, "did not see workspace updated")
			},
		},
	}

	server := framework.SharedKcpServer(t)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			cfg := server.BaseConfig(t)

			orgPath, _ := framework.NewOrganizationFixture(t, server)

			// create clients
			kcpClient, err := kcpclusterclientset.NewForConfig(cfg)
			require.NoError(t, err)

			orgExpect, err := framework.ExpectWorkspaces(ctx, t, kcpClient.Cluster(orgPath))
			require.NoError(t, err, "failed to start a workspace expecter")

			rootExpectShard, err := framework.ExpectWorkspaceShards(ctx, t, kcpClient.Cluster(core.RootCluster.Path()))
			require.NoError(t, err, "failed to start a shard expecter")

			testCase.work(ctx, t, runningServer{
				RunningServer:          server,
				rootWorkspaceKcpClient: kcpClient.Cluster(core.RootCluster.Path()),
				orgWorkspaceKcpClient:  kcpClient.Cluster(orgPath),
				orgExpect:              orgExpect,
				rootExpectShard:        rootExpectShard,
			})
		})
	}
}

func toYAML(t *testing.T, obj interface{}) string {
	t.Helper()
	bs, err := yaml.Marshal(obj)
	require.NoError(t, err, "failed to marshal object")
	return string(bs)
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

func scheduledAnywhere(object *tenancyv1alpha1.Workspace) error {
	if isUnschedulable(object) {
		return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	if object.Spec.Cluster == "" {
		return fmt.Errorf("expected workspace.spec.cluster to be anything, got %q", object.Spec.Cluster)
	}
	return nil
}
