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

package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/syncer/dns/workspace1"
	"github.com/kcp-dev/kcp/test/e2e/syncer/dns/workspace2"
)

func TestDNSResolution(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster:requires-kind")

	if len(framework.TestConfig.PClusterKubeconfig()) == 0 {
		t.Skip("Test requires a pcluster")
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamServer := framework.SharedKcpServer(t)

	upstreamConfig := upstreamServer.BaseConfig(t)

	orgWorkspace := framework.NewOrganizationFixture(t, upstreamServer)

	locationWorkspace := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("location"))

	workloadWorkspace1 := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("workload-1"))
	workloadWorkspace2 := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("workload-2"))

	syncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspace,
		framework.WithSyncedUserWorkspaces(workloadWorkspace1, workloadWorkspace2),
	).Start(t)
	syncer.WaitForClusterReady(ctx, t)

	downstreamKubeClient, err := kubernetes.NewForConfig(syncer.DownstreamConfig)
	require.NoError(t, err)

	t.Logf("Bind workload workspace 1 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace1.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
	).Bind(t)

	t.Logf("Bind workload workspace 2 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace2.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
	).Bind(t)

	err = framework.CreateResources(t, ctx, workspace1.FS, upstreamConfig, workloadWorkspace1.Path())
	require.NoError(t, err)

	err = framework.CreateResources(t, ctx, workspace2.FS, upstreamConfig, workloadWorkspace2.Path())
	require.NoError(t, err)

	downstreamWS1NS1 := syncer.DownstreamNamespaceFor(t, workloadWorkspace1, "dns-ws1-ns1")
	t.Logf("Downstream namespace 1 in workspace 1 is %s", downstreamWS1NS1)

	downstreamWS1NS2 := syncer.DownstreamNamespaceFor(t, workloadWorkspace1, "dns-ws1-ns2")
	t.Logf("Downstream namespace 2 in workspace 1 is %s", downstreamWS1NS2)

	downstreamWS2NS1 := syncer.DownstreamNamespaceFor(t, workloadWorkspace2, "dns-ws2-ns1")
	t.Logf("Downstream namespace 1 in workspace 2 is %s", downstreamWS2NS1)

	t.Log("Checking fully qualified DNS name resolves")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamWS1NS1, "ping-fully-qualified", "PING svc.dns-ws1-ns1.svc.cluster.local ("),
		wait.ForeverTestTimeout, time.Millisecond*500, "Service name was not resolved")

	t.Log("Checking not qualified DNS name resolves")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamWS1NS1, "ping-not-qualified", "PING svc ("),
		wait.ForeverTestTimeout, time.Millisecond*500, "Service name was not resolved")

	t.Log("Checking DNS name resolves across namespaces in same workspace")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamWS1NS2, "ping-across-namespace", "PING svc.dns-ws1-ns1 ("),
		wait.ForeverTestTimeout, time.Millisecond*500, "Service name was not resolved")

	t.Log("Checking DNS name does not resolve across workspaces")
	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamWS2NS1, "ping-fully-qualified-fail", "ping: bad"),
		wait.ForeverTestTimeout, time.Millisecond*500, "Service name was resolved")
}

func checkLogs(ctx context.Context, t *testing.T, downstreamKubeClient *kubernetes.Clientset, downstreamNamespace, containerName, expectedPrefix string) func() (success bool, reason string) {
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
