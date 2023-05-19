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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/syncer/dns/workspace1"
	"github.com/kcp-dev/kcp/test/e2e/syncer/dns/workspace2"
)

func TestDNSResolution(t *testing.T) {
	t.Skip("Test is flaky, and we are going to remove the syncer soon anyway")

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

	workloadWorkspace1Path, workloadWorkspace1 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("workload-1"), framework.TODO_WithoutMultiShardSupport())
	workloadWorkspace2Path, workloadWorkspace2 := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.WithName("workload-2"), framework.TODO_WithoutMultiShardSupport())

	syncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspacePath,
		framework.WithSyncedUserWorkspaces(workloadWorkspace1, workloadWorkspace2),
	).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)
	syncer.WaitForSyncTargetReady(ctx, t)

	downstreamKubeClient, err := kubernetes.NewForConfig(syncer.DownstreamConfig)
	require.NoError(t, err)

	t.Logf("Bind workload workspace 1 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace1Path, upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspacePath),
	).Bind(t)

	t.Logf("Bind workload workspace 2 to location workspace")
	framework.NewBindCompute(t, workloadWorkspace2Path, upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspacePath),
	).Bind(t)

	err = framework.CreateResources(ctx, workspace1.FS, upstreamConfig, workloadWorkspace1Path)
	require.NoError(t, err)

	err = framework.CreateResources(ctx, workspace2.FS, upstreamConfig, workloadWorkspace2Path)
	require.NoError(t, err)

	downstreamWS1NS1 := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace1.Spec.Cluster), "dns-ws1-ns1")
	t.Logf("Downstream namespace 1 in workspace 1 is %s", downstreamWS1NS1)

	downstreamWS1NS2 := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace1.Spec.Cluster), "dns-ws1-ns2")
	t.Logf("Downstream namespace 2 in workspace 1 is %s", downstreamWS1NS2)

	downstreamWS2NS1 := syncer.DownstreamNamespaceFor(t, logicalcluster.Name(workloadWorkspace2.Spec.Cluster), "dns-ws2-ns1")
	t.Logf("Downstream namespace 1 in workspace 2 is %s", downstreamWS2NS1)

	t.Log("Checking network policies have been created")
	framework.Eventually(t, func() (success bool, reason string) {
		np, err := downstreamKubeClient.NetworkingV1().NetworkPolicies(syncer.SyncerID).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error while getting network policies: %v\n", err)
		}
		if len(np.Items) != 2 {
			return false, fmt.Sprintf("expecting 2 network policies, got: %d\n", len(np.Items))
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "Network policies haven't been created")

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

	t.Log("Change ping-fully-qualified deployment DNS config to use workspace 2 nameserver and check the DNS name does not resolve")
	dnsServices, err := downstreamKubeClient.CoreV1().Services(syncer.SyncerID).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.True(t, len(dnsServices.Items) >= 2)

	deployment, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS1).Get(ctx, "ping-fully-qualified", metav1.GetOptions{})
	require.NoError(t, err)

	existingDNSIP := deployment.Spec.Template.Spec.DNSConfig.Nameservers[0]
	newDNSIP := ""
	for _, svc := range dnsServices.Items {
		if strings.HasPrefix(svc.Name, "kcp-dns-") {
			if svc.Spec.ClusterIP != existingDNSIP {
				newDNSIP = svc.Spec.ClusterIP
				break
			}
		}
	}
	require.NotEmpty(t, newDNSIP, "could not find another DNS service")
	deployment.Spec.Template.Spec.DNSConfig.Nameservers[0] = newDNSIP
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS1).Get(ctx, "ping-fully-qualified", metav1.GetOptions{})
		if err != nil {
			return err
		}

		deployment.Spec.Template.Spec.DNSConfig.Nameservers[0] = newDNSIP
		_, err = downstreamKubeClient.AppsV1().Deployments(downstreamWS1NS1).Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	framework.Eventually(t, checkLogs(ctx, t, downstreamKubeClient, downstreamWS1NS1, "ping-fully-qualified", "ping: bad"),
		wait.ForeverTestTimeout, time.Millisecond*500, "Service name was resolved")
}

//nolint:unused
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
