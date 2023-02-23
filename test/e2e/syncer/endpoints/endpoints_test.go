/*
Copyright 2023 The KCP Authors.

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

package endpoints

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestEndpointsUpsyncing(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster:requires-kind")

	if len(framework.TestConfig.PClusterKubeconfig()) == 0 {
		t.Skip("Test requires a pcluster")
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamServer := framework.SharedKcpServer(t)

	upstreamConfig := upstreamServer.BaseConfig(t)

	orgPath, _ := framework.NewOrganizationFixture(t, upstreamServer, framework.TODO_WithoutMultiShardSupport())

	locationWorkspacePath, _ := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("location"), framework.TODO_WithoutMultiShardSupport())

	workloadWorkspacePath, workloadWorkspace := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("workload"), framework.TODO_WithoutMultiShardSupport())

	upstreamKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	syncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspacePath,
		framework.WithSyncedUserWorkspaces(workloadWorkspace),
	).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)
	syncer.WaitForSyncTargetReady(ctx, t)

	downstreamKubeClient, err := kubernetes.NewForConfig(syncer.DownstreamConfig)
	require.NoError(t, err)

	t.Logf("Bind workload workspace to location workspace")
	framework.NewBindCompute(t, workloadWorkspacePath, upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspacePath),
	).Bind(t)

	err = framework.CreateResources(ctx, FS, upstreamConfig, workloadWorkspacePath)
	require.NoError(t, err)

	downstreamNamespace := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace.Spec.Cluster), "default")
	t.Logf("Downstream namespace is %s", downstreamNamespace)

	t.Log("Checking services have been created downstream")
	framework.Eventually(t, func() (success bool, reason string) {
		services, err := downstreamKubeClient.CoreV1().Services(downstreamNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting services: %v\n", err)
		}
		if len(services.Items) != 2 {
			return false, fmt.Sprintf("expecting 2 services, got: %d\n", len(services.Items))
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Services haven't been created")

	t.Log("Checking endpoints have been created downstream")
	framework.Eventually(t, func() (success bool, reason string) {
		endpoints, err := downstreamKubeClient.CoreV1().Endpoints(downstreamNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting endpoints: %v\n", err)
		}
		if len(endpoints.Items) != 2 {
			return false, fmt.Sprintf("expecting 2 endpoints, got: %d\n", len(endpoints.Items))
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Endpoints haven't been created")

	t.Log("Checking that only 1 endpoint has been upsynced upstream")
	framework.Eventually(t, func() (success bool, reason string) {
		endpoints, err := upstreamKubeClusterClient.Cluster(workloadWorkspacePath).CoreV1().Endpoints("default").List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting endpoints: %v\n", err)
		}
		if len(endpoints.Items) != 1 {
			return false, fmt.Sprintf("expecting 1 endpoint, got: %d\n", len(endpoints.Items))
		}

		if endpoints.Items[0].Name != "with-endpoints-upsync" {
			return false, fmt.Sprintf("expecting endpoint with name 'with-endpoints-upsync', got: %s\n", endpoints.Items[0].Name)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Endpoints hasn't been upsynced")
}
