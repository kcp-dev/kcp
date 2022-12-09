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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	"k8s.io/client-go/kubernetes"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceShardController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	type runningServer struct {
		framework.RunningServer
		rootShardClient               tenancyv1alpha1client.ClusterWorkspaceShardInterface
		rootKubeClient, orgKubeClient kubernetes.Interface
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

			kcpClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err)

			expecterClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
			require.NoError(t, err)

			expect, err := framework.ExpectWorkspaceShards(ctx, t, expecterClient.Cluster(orgClusterName.Path()))
			require.NoError(t, err, "failed to start expecter")

			kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kube rootShardClient for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootShardClient: kcpClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaceShards(),
				rootKubeClient:  kubeClient.Cluster(tenancyv1alpha1.RootCluster.Path()),
				orgKubeClient:   kubeClient.Cluster(orgClusterName.Path()),
				expect:          expect,
			})
		})
	}
}
