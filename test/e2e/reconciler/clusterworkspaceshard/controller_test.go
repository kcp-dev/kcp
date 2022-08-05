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

package workspaceshard

import (
	"context"
	"testing"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/stretchr/testify/require"

	kubernetesclientset "k8s.io/client-go/kubernetes"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceShardController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		rootShardClient               tenancyv1alpha1client.ClusterWorkspaceShardInterface
		rootKubeClient, orgKubeClient kubernetesclientset.Interface
		expect                        framework.RegisterWorkspaceShardExpectation
	}
	var testCases = []struct {
		name        string
		destructive bool
		work        func(ctx context.Context, t *testing.T, server runningServer)
	}{
		// nothing for now
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

			orgClusterCfg := kcpclienthelper.ConfigWithCluster(cfg, orgClusterName)
			orgClusterKcpClient, err := kubernetesclientset.NewForConfig(orgClusterCfg)
			require.NoError(t, err)

			rootClusterCfg := kcpclienthelper.ConfigWithCluster(cfg, tenancyv1alpha1.RootCluster)
			rootClusterKcpClient, err := kcpclientset.NewForConfig(rootClusterCfg)
			require.NoError(t, err)

			expect, err := framework.ExpectWorkspaceShards(ctx, t, rootClusterKcpClient)
			require.NoError(t, err, "failed to start expecter")

			rootKubeClient, err := kubernetesclientset.NewForConfig(rootClusterCfg)
			require.NoError(t, err, "failed to construct kube rootShardClient for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootShardClient: rootClusterKcpClient.TenancyV1alpha1().ClusterWorkspaceShards(),
				rootKubeClient:  rootKubeClient,
				orgKubeClient:   orgClusterKcpClient,
				expect:          expect,
			})
		})
	}
}
