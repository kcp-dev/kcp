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
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/syncer/networkpolicies/workspace1"
)

func TestNetworkPolicies(t *testing.T) {
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

	workloadWorkspace1Path, workloadWorkspace1 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("workload"), framework.TODO_WithoutMultiShardSupport())
	workloadWorkspace2Path, workloadWorkspace2 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("workload"), framework.TODO_WithoutMultiShardSupport())

	syncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspacePath,
		framework.WithSyncedUserWorkspaces(workloadWorkspace1),
		framework.WithSyncedUserWorkspaces(workloadWorkspace2),
	).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)
	syncer.WaitForSyncTargetReady(ctx, t)

	downstreamKubeClient, err := kubernetes.NewForConfig(syncer.DownstreamConfig)
	require.NoError(t, err)

	upstreamKubeClient, err := kubernetes.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	t.Logf("Bind workload workspace 1 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace1Path, upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspacePath),
	).Bind(t)

	err = framework.CreateResources(ctx, workspace1.FS, upstreamConfig, workloadWorkspace1Path)
	require.NoError(t, err)

	downstreamNamespace1 := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace1.Spec.Cluster), "default")
	t.Logf("Downstream namespace for workspace 1 is %s", downstreamNamespace1)

	t.Log("Checking services in workspace 1 have been created downstream")
	var service corev1.Service
	framework.Eventually(t, func() (success bool, reason string) {
		services, err := downstreamKubeClient.CoreV1().Services(downstreamNamespace1).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting services: %v\n", err)
		}
		if len(services.Items) != 1 {
			return false, fmt.Sprintf("expecting 1 service, got: %d\n", len(services.Items))
		}

		service = services.Items[0]
		if service.Spec.ClusterIP == "" {
			return false, "service cluster IP is empty"
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Service in workspace 1 haven't been created")

	t.Logf("Bind workload workspace 2 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace2Path, upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspacePath),
	).Bind(t)

	downstreamNamespace2 := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace2.Spec.Cluster), "default")
	t.Logf("Downstream namespace for workspace 2 is %s", downstreamNamespace2)

	_, err = upstreamKubeClient.CoreV1().ConfigMaps("default").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ip"}, Data: map[string]string{"ip": service.Spec.ClusterIP}}, metav1.CreateOptions{})
	require.NoError(t, err)

	framework.Eventually(t, func() (success bool, reason string) {
		deployments, err := upstreamKubeClient.AppsV1().Deployments(downstreamNamespace2).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting deployments: %v\n", err)
		}
		if len(deployments.Items) != 1 {
			return false, fmt.Sprintf("expecting 1 deployment, got: %d\n", len(deployments.Items))
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Service in workspace 1 haven't been created")

	t.Logf("Check k8s APIServer is reachable")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamNamespace1, "ping-apiserver", "PING"),
		wait.ForeverTestTimeout, time.Millisecond*500, "APIServer is not reachable")

	t.Logf("Check out-of-cluster host is reachable")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamNamespace1, "ping-external", "PING"),
		wait.ForeverTestTimeout, time.Millisecond*500, "External host is not reachable")

	t.Logf("Check other tenant pod is not reachable")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamNamespace2, "ping-other-tenant-fail", "ping: bad"),
		wait.ForeverTestTimeout, time.Millisecond*500, "Other tenant pod was reachable")
}

func checkLogs(ctx context.Context, t *testing.T, downstreamKubeClient *kubernetes.Clientset, downstreamNamespace, containerName, expectedPrefix string) func() (success bool, reason string) {
	t.Helper()

	return func() (success bool, reason string) {
		pods, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error getting pods: %v", err)
		}

		for _, pod := range pods.Items {
			for _, c := range pod.Spec.Containers {
				if c.Name == containerName {
					res, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
						Container: c.Name,
					}).DoRaw(ctx)

					if err != nil {
						return false, fmt.Sprintf("Failed to get logs for pod %s/%s container %s: %v", pod.Namespace, pod.Name, c.Name, err)
					}

					for _, line := range strings.Split(string(res), "\n") {
						t.Logf("Pod %s/%s container %s: %s", pod.Namespace, pod.Name, c.Name, line)
						if strings.HasPrefix(line, expectedPrefix) {
							return true, ""
						}
					}
				}
			}
		}
		return false, "no pods"
	}
}
