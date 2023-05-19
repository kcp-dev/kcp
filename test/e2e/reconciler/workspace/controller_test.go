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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
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
				workspace, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				framework.EventuallyCondition(t, func() (utilconditions.Getter, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				}, framework.Is(tenancyv1alpha1.WorkspaceScheduled))
			},
		},
		{
			name:        "add a shard after a workspace is unschedulable, expect it to be scheduled",
			destructive: true,
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Helper()
				var previouslyValidShard corev1alpha1.Shard
				t.Logf("Get a list of current shards so that we can schedule onto a valid shard later")
				shards, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				if len(shards.Items) == 0 {
					t.Fatalf("expected to get some shards but got none")
				}
				previouslyValidShard = shards.Items[0]
				t.Logf("Delete all pre-configured shards, we have to control the creation of the workspace shards in this test")
				err = server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
				require.NoError(t, err)

				t.Logf("Create a workspace without shards")
				workspace, err := server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "steve"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be unschedulable")
				framework.EventuallyCondition(t, func() (utilconditions.Getter, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				}, framework.IsNot(tenancyv1alpha1.WorkspaceScheduled).WithReason(tenancyv1alpha1.WorkspaceReasonUnschedulable))

				t.Logf("Add previously removed shard %q", previouslyValidShard.Name)
				newShard, err := server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Create(ctx, &corev1alpha1.Shard{
					ObjectMeta: metav1.ObjectMeta{
						Name:   previouslyValidShard.Name,
						Labels: previouslyValidShard.Labels,
					},
					Spec: corev1alpha1.ShardSpec{
						BaseURL:     previouslyValidShard.Spec.BaseURL,
						ExternalURL: previouslyValidShard.Spec.ExternalURL,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")
				server.RunningServer.Artifact(t, func() (runtime.Object, error) {
					return server.rootWorkspaceKcpClient.CoreV1alpha1().Shards().Get(ctx, newShard.Name, metav1.GetOptions{})
				})

				t.Logf("Expect workspace to be scheduled to the shard and show the external URL")
				framework.EventuallyCondition(t, func() (utilconditions.Getter, error) {
					return server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				}, framework.Is(tenancyv1alpha1.WorkspaceScheduled))

				workspace, err = server.orgWorkspaceKcpClient.TenancyV1alpha1().Workspaces().Get(ctx, workspace.Name, metav1.GetOptions{})
				require.NoError(t, err)
				orgLogicalCluster, err := server.orgWorkspaceKcpClient.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
				require.NoError(t, err)
				path := fmt.Sprintf("%s:%s", orgLogicalCluster.Annotations[core.LogicalClusterPathAnnotationKey], workspace.Name)
				require.Emptyf(t, cmp.Diff(previouslyValidShard.Spec.BaseURL+"/clusters/"+path, workspace.Spec.URL), "incorrect URL")
			},
		},
	}

	sharedServer := framework.SharedKcpServer(t)

	for _, testCase := range testCases {
		testCase := testCase
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

			orgPath, _ := framework.NewOrganizationFixture(t, server)

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
