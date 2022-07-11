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
	"net/url"
	"path"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestUserHomeWorkspaces(t *testing.T) {
	type clientInfo struct {
		Token string
	}

	type runningServer struct {
		framework.RunningServer
		orgClusterName                logicalcluster.Name
		kubeClusterClient             kubernetes.ClusterInterface
		kcpClusterClient              kcpclientset.ClusterInterface
		kcpUserClusterClients         []kcpclientset.ClusterInterface
		virtualPersonalClusterClients []kcpclientset.ClusterInterface
	}

	var testCases = []struct {
		name        string
		clientInfos []clientInfo
		work        func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "Create a workspace in the non-existing home and have it created automatically",
			clientInfos: []clientInfo{
				{
					Token: "user-1-token",
				},
				{
					Token: "user-2-token",
				},
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualPersonalClusterClients[0]
				kcpUser1Client := server.kcpUserClusterClients[0]

				vwUser2Client := server.virtualPersonalClusterClients[1]

				t.Logf("Get ~ Home workspace URL for user-1")
				nonExistingHomeWorkspace, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should be allowed to get his home wporkspace even before it exists")
				require.NotNil(t, nonExistingHomeWorkspace, "home workspace should not be nil, even before it exists")
				require.Equal(t, metav1.Time{}, nonExistingHomeWorkspace.CreationTimestamp, "Non-existing home workspace should not have a creation timestamp")
				require.Equal(t, tenancyv1alpha1.ClusterWorkspacePhaseType(""), nonExistingHomeWorkspace.Status.Phase, "Non-existing home workspace should have an empty phase")

				homeWorkspaceURL := nonExistingHomeWorkspace.Status.URL
				u, err := url.Parse(homeWorkspaceURL)
				require.NoError(t, err)
				homeWorkspaceName := logicalcluster.New(path.Base(u.Path))
				t.Logf("Home workspace for user-1 is: %s", homeWorkspaceName)

				require.Equal(t, "user-1", homeWorkspaceName.Base(), "Home workspace for the wrong user")

				t.Logf("Create Workspace workspace1 in the user-1 non-existing home as user-1")
				_, err = vwUser1Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "user-1 should be able to create a workspace inside his home workspace even though it doesn't exist")

				existingHomeWorkspace, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should get his home workspace through '~' even after it exists")
				require.NotNil(t, nonExistingHomeWorkspace, "home workspace should not be nil, even after it exists")
				require.NotEqual(t, metav1.Time{}, existingHomeWorkspace.CreationTimestamp, "Existing home workspace retrieved from '~' should have a valid creation timestamp")
				require.Equal(t, tenancyv1alpha1.ClusterWorkspacePhaseReady, existingHomeWorkspace.Status.Phase, "The existing home workspace (created automatically), when retrieved from '~', should be READY")

				t.Logf("Workspace will show up in list of workspaces of user-1")
				require.Eventually(t, func() bool {
					list, err := vwUser1Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
					if err != nil {
						t.Logf("failed to get workspaces: %v", err)
					}
					return len(list.Items) == 1 && list.Items[0].Name == "workspace1"
				}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to list workspace1")

				t.Logf("user-2 doesn't have the right to acces user-1 home workspace")
				_, err = vwUser2Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev is forbidden: "list" workspace "" in workspace "root:users:bi:ie:user-1" is not allowed`, "user-1 should be able to create a workspace inside his home workspace even though it doesn't exist")

			},
		},
		{
			name: "Cannot trigger automatic creation of a Home workspace for the wrong user",
			clientInfos: []clientInfo{
				{
					Token: "user-1-token",
				},
				{
					Token: "user-2-token",
				},
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				kcpUser1Client := server.kcpUserClusterClients[0]

				vwUser2Client := server.virtualPersonalClusterClients[1]

				t.Logf("Get ~ Home workspace URL for user-1")
				nonExistingHomeWorkspace, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should be allowed to get his home wporkspace even before it exists")
				require.NotNil(t, nonExistingHomeWorkspace, "home workspace should not be nil, even before it exists")
				require.Equal(t, metav1.Time{}, nonExistingHomeWorkspace.CreationTimestamp, "Non-existing home workspace should not have a creation timestamp")
				require.Equal(t, tenancyv1alpha1.ClusterWorkspacePhaseType(""), nonExistingHomeWorkspace.Status.Phase, "Non-existing home workspace should have an empty phase")

				homeWorkspaceURL := nonExistingHomeWorkspace.Status.URL
				u, err := url.Parse(homeWorkspaceURL)
				require.NoError(t, err)
				homeWorkspaceName := logicalcluster.New(path.Base(u.Path))
				t.Logf("Home workspace for user-1 is: %s", homeWorkspaceName)

				require.Equal(t, "user-1", homeWorkspaceName.Base(), "Home workspace for the wrong user")

				t.Logf("Try to create Workspace workspace1 in the non-existing user-1 home as user-2")
				_, err = vwUser2Client.Cluster(homeWorkspaceName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.EqualError(t, err, `workspaces.tenancy.kcp.dev is forbidden: "create" workspace "" in workspace "root:users:bi:ie:user-1" is not allowed`, "user-2 should be not able to trigger the automatic creation of user-1 home")
			},
		},
		{
			name: "Cannot trigger automatic creation of a Home workspace for the wrong user, even as system-masters",
			clientInfos: []clientInfo{
				{
					Token: "user-1-token",
				},
				{
					Token: "user-2-token",
				},
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				kcpUser1Client := server.kcpUserClusterClients[0]

				t.Logf("Get ~ Home workspace URL for user-1")
				nonExistingHomeWorkspace, err := kcpUser1Client.Cluster(tenancyv1alpha1.RootCluster).TenancyV1beta1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
				require.NoError(t, err, "user-1 should be allowed to get his home wporkspace even before it exists")
				require.NotNil(t, nonExistingHomeWorkspace, "home workspace should not be nil, even before it exists")
				require.Equal(t, metav1.Time{}, nonExistingHomeWorkspace.CreationTimestamp, "Non-existing home workspace should not have a creation timestamp")
				require.Equal(t, tenancyv1alpha1.ClusterWorkspacePhaseType(""), nonExistingHomeWorkspace.Status.Phase, "Non-existing home workspace should have an empty phase")

				homeWorkspaceURL := nonExistingHomeWorkspace.Status.URL
				u, err := url.Parse(homeWorkspaceURL)
				require.NoError(t, err)
				homeWorkspaceName := logicalcluster.New(path.Base(u.Path))
				t.Logf("Home workspace for user-1 is: %s", homeWorkspaceName)

				require.Equal(t, "user-1", homeWorkspaceName.Base(), "Home workspace for the wrong user")

				t.Logf("Try to create a ClusterWorkspace workspace1 in the non-existing user-1 home as system:masters")
				_, err = server.kcpClusterClient.Cluster(homeWorkspaceName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workspace1",
					},
				}, metav1.CreateOptions{})
				require.EqualError(t, err, `clusterworkspaces.tenancy.kcp.dev "~" is forbidden: User "system:apiserver" cannot create resource "clusterworkspaces/workspace" in API group "tenancy.kcp.dev" at the cluster scope: workspace access not permitted`, "system:master should be not able to trigger the automatic creation of user-1 home")
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			tokenAuthFile := framework.WriteTokenAuthFile(t)
			server := framework.PrivateKcpServer(t, framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)...)
			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// create non-virtual clients
			kcpConfig := server.DefaultConfig(t)
			kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")
			kcpClusterClient, err := kcpclientset.NewClusterForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			// create kcp client and virtual clients for all users requested
			var kcpUserClusterClients []kcpclientset.ClusterInterface
			var virtualPersonalClusterClients []kcpclientset.ClusterInterface
			for _, ci := range testCase.clientInfos {
				userConfig := framework.ConfigWithToken(ci.Token, rest.CopyConfig(kcpConfig))
				virtualPersonalClusterClients = append(virtualPersonalClusterClients, &virtualClusterClient{scope: "personal", config: userConfig})
				kcpUserClusterClient, err := kcpclientset.NewClusterForConfig(userConfig)
				require.NoError(t, err)
				kcpUserClusterClients = append(kcpUserClusterClients, kcpUserClusterClient)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                 server,
				orgClusterName:                orgClusterName,
				kubeClusterClient:             kubeClusterClient,
				kcpClusterClient:              kcpClusterClient,
				kcpUserClusterClients:         kcpUserClusterClients,
				virtualPersonalClusterClients: virtualPersonalClusterClients,
			})
		})
	}
}

type virtualClusterClient struct {
	scope  string
	config *rest.Config
}

func (c *virtualClusterClient) Cluster(cluster logicalcluster.Name) kcpclientset.Interface {
	config := rest.CopyConfig(c.config)
	config.Host += path.Join(virtualoptions.DefaultRootPathPrefix, "workspaces", cluster.String(), c.scope)
	return kcpclientset.NewForConfigOrDie(config)
}
