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

package homeworkspaces

import (
	"context"
	"path"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestUserHomeWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type clientInfo struct {
		Token string
	}

	type runningServer struct {
		framework.RunningServer
		kubeClusterClient             kcpkubernetesclientset.ClusterInterface
		rootShardKcpClusterClient     kcpclusterclientset.ClusterInterface
		kcpUserClusterClients         []kcpclusterclientset.ClusterInterface
		virtualPersonalClusterClients []VirtualClusterClient
	}

	var testCases = []struct {
		name    string
		kcpArgs []string
		work    func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "Create a workspace in the non-existing home and have it created automatically through ~",
			kcpArgs: []string{
				"--home-workspaces-home-creator-groups",
				"team-1",
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				kcpUser1Client := server.kcpUserClusterClients[0]
				kcpUser2Client := server.kcpUserClusterClients[1]

				t.Logf("Get ~ Home workspace URL for user-1")
				createdHome, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should be able to get ~ workspace")
				require.NotEqual(t, metav1.Time{}, createdHome.CreationTimestamp, "should have a creation timestamp, i.e. is not virtual")
				require.Equal(t, tenancyv1alpha1.WorkspacePhaseReady, createdHome.Status.Phase, "created home workspace should be ready")

				t.Logf("Get ~ Home workspace URL for user-2")

				_, err = kcpUser2Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev "~" is forbidden: User "user-2" cannot create resource "workspaces" in API group "tenancy.kcp.dev" at the cluster scope: access denied`, "user-2 should not be allowed to get his home workspace even before it exists")
			},
		},
		{
			name: "Create a workspace in the non-existing home and have it created automatically in-workspace request",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualPersonalClusterClients[0]
				kcpUser1Client := server.kcpUserClusterClients[0]

				vwUser2Client := server.virtualPersonalClusterClients[1]

				t.Logf("Create Workspace workspace1 in the user-1 non-existing home as user-1")
				homeWorkspaceName := logicalcluster.New("root:users:bi:ie:user-1")
				_, err := vwUser1Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "user-1 should be able to create a workspace inside his home workspace even though it doesn't exist")

				t.Logf("Get ~ workspace of user-1")
				existingHomeWorkspace, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should get his home workspace through ~")
				require.Equal(t, tenancyv1alpha1.WorkspacePhaseReady, existingHomeWorkspace.Status.Phase, "created home workspace should be ready")

				t.Logf("Workspace will show up in list of workspaces of user-1")
				require.Eventually(t, func() bool {
					list, err := vwUser1Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
					if err != nil {
						t.Logf("failed to get workspaces: %v", err)
					}
					return len(list.Items) == 1 && list.Items[0].Name == "workspace1"
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspace1")

				t.Logf("user-2 doesn't have the right to access user-1 home workspace")
				_, err = vwUser2Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev is forbidden: User "user-2" cannot list resource "workspaces" in API group "tenancy.kcp.dev" at the cluster scope: access denied`, "user-1 should be able to create a workspace inside his home workspace even though it doesn't exist")
			},
		},
		{
			name: "Cannot trigger automatic creation of a Home workspace for the wrong user",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser2Client := server.virtualPersonalClusterClients[1]

				t.Logf("Try to create Workspace workspace1 in the non-existing user-1 home as user-2")
				homeWorkspaceName := logicalcluster.New("root:users:bi:ie:user-1")
				_, err := vwUser2Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev is forbidden: User "user-2" cannot create resource "workspaces" in API group "tenancy.kcp.dev" at the cluster scope`, "user-2 should be not able to trigger the automatic creation of user-1 home")
			},
		},
		{
			name: "Cannot trigger automatic creation of a Home workspace for the wrong user, even as system-masters",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("Try to create a ClusterWorkspace workspace1 in the non-existing user-1 home as system:masters")
				homeWorkspaceName := logicalcluster.New("root:users:bi:ie:user-1")
				_, err := server.rootShardKcpClusterClient.Cluster(homeWorkspaceName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev "~" is forbidden: User "shard-admin" cannot create resource "workspaces" in API group "tenancy.kcp.dev" at the cluster scope: workspace access not permitted`, "system:master should be not able to trigger the automatic creation of user-1 home")
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tokenAuthFile := framework.WriteTokenAuthFile(t)
			server := framework.PrivateKcpServer(t, framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile), testCase.kcpArgs...)...))
			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			// create non-virtual clients
			kcpConfig := server.BaseConfig(t)
			rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
			kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			rootShardKcpClusterClient, err := kcpclusterclientset.NewForConfig(rootShardCfg)
			require.NoError(t, err, "failed to construct client for server")

			// create kcp client and virtual clients for all users requested
			var kcpUserClusterClients []kcpclusterclientset.ClusterInterface
			var virtualPersonalClusterClients []VirtualClusterClient
			for _, ci := range []clientInfo{{Token: "user-1-token"}, {Token: "user-2-token"}} {
				userConfig := framework.ConfigWithToken(ci.Token, rest.CopyConfig(kcpConfig))
				virtualPersonalClusterClients = append(virtualPersonalClusterClients, &virtualClusterClient{config: userConfig})
				kcpUserClusterClient, err := kcpclusterclientset.NewForConfig(userConfig)
				require.NoError(t, err)
				kcpUserClusterClients = append(kcpUserClusterClients, kcpUserClusterClient)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                 server,
				kubeClusterClient:             kubeClusterClient,
				rootShardKcpClusterClient:     rootShardKcpClusterClient,
				kcpUserClusterClients:         kcpUserClusterClients,
				virtualPersonalClusterClients: virtualPersonalClusterClients,
			})
		})
	}
}

type VirtualClusterClient interface {
	Cluster(cluster logicalcluster.Name) kcpclientset.Interface
}

type virtualClusterClient struct {
	config *rest.Config
}

func (c *virtualClusterClient) Cluster(cluster logicalcluster.Name) kcpclientset.Interface {
	config := rest.CopyConfig(c.config)
	config.Host += path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", cluster.String())
	return kcpclientset.NewForConfigOrDie(config)
}
