/*
Copyright 2021 The kcp Authors.

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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		kcptestingserver.RunningServer
		rootWorkspaceKcpClient, orgWorkspaceKcpClient kcpclientset.Interface
	}
	var testCases = []struct {
		name        string
		destructive bool
		work        func(ctx context.Context, t *testing.T, server runningServer)
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
				var workspace *tenancyv1alpha1.Workspace
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					var err error
					workspace, err = server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
					require.NoError(c, err, "failed to create workspace")
				}, wait.ForeverTestTimeout, 100*time.Millisecond)
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				}, kcptestinghelpers.Is(tenancyv1alpha1.WorkspaceScheduled))
			},
		},
		{
			name:        "mark shard unschedulable then schedulable again, expect workspace to be scheduled",
			destructive: true,
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()

				t.Logf("Get the root shard")
				shard, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, "root", metav1.GetOptions{})
				require.NoError(t, err)

				t.Logf("Mark the root shard as unschedulable")
				if shard.Annotations == nil {
					shard.Annotations = map[string]string{}
				}
				shard.Annotations["experimental.core.kcp.io/unschedulable"] = "true"
				_, err = server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
				require.NoError(t, err)

				// Workspaces cannot become "unschedulable" - once they
				// are scheduled on a shard this state does not change.
				// This is at the moment of writing by design as
				// workspaces cannot be moved from the shard they are
				// scheduled on and also cannot be moved automatically.
				//
				// Due to this there is a subtle race here - marking the
				// shard unschedulable takes a bit to reflect in the
				// cache used by the reconciler when scheduling
				// workspaces.
				// To account for this the test keeps creating
				// workspaces until one is unschedulable and continues
				// the test with that one.

				t.Logf("Create a workspace while the shard is unschedulable and wait for it to be unschedulable")
				var workspaceName string
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					ws, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{GenerateName: "steve-"}}, metav1.CreateOptions{})
					require.NoError(c, err)
					workspaceName = ws.Name

					// Wait until WorkspaceScheduled is present on the workspace
					require.NoError(c, wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, wait.ForeverTestTimeout, true, func(ctx context.Context) (bool, error) {
						var err error
						ws, err = server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
						if err != nil {
							return false, err
						}

						cond := utilconditions.Get(ws, tenancyv1alpha1.WorkspaceScheduled)
						if cond == nil {
							// WorkspaceScheduled condition not yet set on the workspace, continue looping on this workspace
							return false, nil
						}
						// WorkspaceScheduled is set, continue the check outside of this poll loop
						return true, nil
					}))

					cond := utilconditions.Get(ws, tenancyv1alpha1.WorkspaceScheduled)
					require.Equalf(c, tenancyv1alpha1.WorkspaceReasonUnschedulable, cond.Reason, "workspace %s was not marked unschedulable", ws.Name)
				}, wait.ForeverTestTimeout, time.Second)

				// Now workspaceName is the name of a workspace that is marked unschedulable
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
				})

				t.Logf("Remove unschedulable annotation from the root shard")
				shard, err = server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, "root", metav1.GetOptions{})
				require.NoError(t, err)
				delete(shard.Annotations, "experimental.core.kcp.io/unschedulable")
				shard, err = server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
				require.NoError(t, err)

				t.Logf("Expect workspace to be scheduled to the shard and show the external URL")
				kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
				}, kcptestinghelpers.Is(tenancyv1alpha1.WorkspaceScheduled))

				workspace, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
				require.NoError(t, err)
				orgLogicalCluster, err := server.orgWorkspaceKcpClient.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
				require.NoError(t, err)
				path := fmt.Sprintf("%s:%s", orgLogicalCluster.Annotations[core.LogicalClusterPathAnnotationKey], workspace.Name)
				require.Emptyf(t, cmp.Diff(shard.Spec.BaseURL+"/clusters/"+path, workspace.Spec.URL), "incorrect URL")
			},
		},
	}

	sharedServer := kcptesting.SharedKcpServer(t)

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			server := sharedServer
			if testCase.destructive {
				server = kcptesting.PrivateKcpServer(t)
			}

			cfg := server.BaseConfig(t)

			orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

			// create clients
			kcpClient, err := kcpclusterclientset.NewForConfig(cfg)
			require.NoError(t, err)

			testCase.work(ctx, t, runningServer{
				RunningServer:          server,
				rootWorkspaceKcpClient: kcpClient.Cluster(core.RootCluster.Path()),
				orgWorkspaceKcpClient:  kcpClient.Cluster(orgPath),
			})
		})
	}
}
