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

			kcpClients, err := kcpclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp rootShardClient for server")

			rootKcpClient := kcpClients.Cluster(tenancyv1alpha1.RootCluster)
			expect, err := framework.ExpectWorkspaceShards(ctx, t, rootKcpClient)
			require.NoError(t, err, "failed to start expecter")

			kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kube rootShardClient for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootShardClient: rootKcpClient.TenancyV1alpha1().ClusterWorkspaceShards(),
				rootKubeClient:  kubeClients.Cluster(tenancyv1alpha1.RootCluster),
				orgKubeClient:   kubeClients.Cluster(orgClusterName),
				expect:          expect,
			})
		})
	}
}
