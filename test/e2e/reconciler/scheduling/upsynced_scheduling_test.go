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

package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestUpsyncedScheduling verifies that the scheduler correctly manages upsynced resources, in order to
// ensure the desired behaviour, this test will:
//
// 1. Setup the basics of the test:
//   - Create two distinct workspaces, a location worskpace and a user workspace.
//   - Simulate the deployment of a syncer which would sync resources from the user workspace to a physical cluster (we only strart the heartbeat and APIImporter parts of the Syncer), without effective syncing.
//
// 2. Upsync a pod from to the user workspace.
// 3. Shutdown the healthchecker of the syncer, and verify that the upsynced pod is still scheduled to the current synctarget as "Upsync"
// 4. Restart the healthchecker of the syncer, and verify that the upsynced pod is still scheduled to the current synctarget as "Upsync"
// 5. Delete the synctarget, and verify that the upsynced pod gets deleted.
func TestUpsyncedScheduling(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamServer := framework.PrivateKcpServer(t, framework.WithCustomArguments("--sync-target-heartbeat-threshold=20s"))
	t.Log("Creating an organization")
	orgPath, _ := framework.NewOrganizationFixture(t, upstreamServer, framework.TODO_WithoutMultiShardSupport())
	t.Log("Creating two workspaces, one for the synctarget and the other for the user workloads")
	synctargetWsPath, synctargetWs := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.TODO_WithoutMultiShardSupport())
	synctargetWsName := logicalcluster.Name(synctargetWs.Spec.Cluster)
	userWsPath, userWs := framework.NewWorkspaceFixture(t, upstreamServer, orgPath, framework.TODO_WithoutMultiShardSupport())
	userWsName := logicalcluster.Name(userWs.Spec.Cluster)

	syncerFixture := framework.NewSyncerFixture(t, upstreamServer, synctargetWsName.Path(),
		framework.WithExtraResources("pods"),
		framework.WithExtraResources("deployments.apps"),
		framework.WithAPIExports("kubernetes"),
		framework.WithSyncedUserWorkspaces(userWs),
	).CreateSyncTargetAndApplyToDownstream(t).StartAPIImporter(t).StartHeartBeat(t)

	t.Log("Binding the consumer workspace to the location workspace")
	framework.NewBindCompute(t, userWsName.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(synctargetWsName.Path()),
		framework.WithAPIExportsWorkloadBindOption(synctargetWsName.String()+":kubernetes"),
	).Bind(t)

	upstreamConfig := upstreamServer.BaseConfig(t)
	upstreamKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	upstreamNamespace, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-scheduling",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	upstreamKcpClient, err := kcpclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	syncTarget, err := upstreamKcpClient.Cluster(synctargetWsPath).WorkloadV1alpha1().SyncTargets().Get(ctx,
		syncerFixture.SyncerConfig.SyncTargetName,
		metav1.GetOptions{},
	)
	require.NoError(t, err)

	t.Log(t, "Wait for being able to list deployments in the consumer workspace via direct access")
	require.Eventually(t, func() bool {
		_, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		return !apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	stateLabelKey := "state.workload.kcp.io/" + workloadv1alpha1.ToSyncTargetKey(synctargetWsName, syncTarget.Name)

	t.Log("Upsyncing Pod to KCP")
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: upstreamNamespace.Name,
			Labels: map[string]string{
				stateLabelKey: "Upsync",
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "test-container",
				},
			},
		},
	}

	// Create a client that uses the upsyncer URL
	upsyncerKCPClient, err := kcpkubernetesclientset.NewForConfig(syncerFixture.UpsyncerVirtualWorkspaceConfig)
	require.NoError(t, err)

	_, err = upsyncerKCPClient.Cluster(userWsName.Path()).CoreV1().Pods(upstreamNamespace.Name).Create(ctx, &pod, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Checking that the upsynced POD has the state set to Upsync...")
	framework.Eventually(t, func() (bool, string) {
		_, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Pods(upstreamNamespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if pod.Labels[stateLabelKey] == "Upsync" {
			return true, ""
		}
		return false, fmt.Sprintf("expected state to be Upsync, got %s", pod.Labels[stateLabelKey])
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected state to be Upsync, got %s", pod.Labels[stateLabelKey])

	t.Log("Stopping the syncer healthchecker...")
	syncerFixture.StopHeartBeat(t)

	t.Log("Checking that the synctarget is not ready...")
	framework.Eventually(t, func() (bool, string) {
		syncTarget, err := upstreamKcpClient.Cluster(synctargetWsPath).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTarget.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if conditions.IsTrue(syncTarget, workloadv1alpha1.HeartbeatHealthy) {
			return false, "expected synctarget to be not ready"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Checking that the upsynced POD remains in the Upsync state...")
	require.Never(t, func() bool {
		_, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Pods(upstreamNamespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return pod.Labels[stateLabelKey] != "Upsync"
	}, 5*time.Second, time.Millisecond*100, "expected state to be Upsync, got %s", pod.Labels[stateLabelKey])

	t.Log("Starting the syncer healthcheck again...")
	syncerFixture.StartHeartBeat(t)

	t.Log("Checking that the upsynced POD remains in the Upsync state...")
	require.Never(t, func() bool {
		_, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Pods(upstreamNamespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return pod.Labels[stateLabelKey] != "Upsync"
	}, 5*time.Second, time.Millisecond*100, "expected state to be Upsync, got %s", pod.Labels[stateLabelKey])

	t.Log("Deleting the Synctarget...")
	err = upstreamKcpClient.Cluster(synctargetWsPath).WorkloadV1alpha1().SyncTargets().Delete(ctx, syncTarget.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Log("Checking that the upsynced Pod has been deleted...")
	framework.Eventually(t, func() (bool, string) {
		_, err := upstreamKubeClusterClient.Cluster(userWsPath).CoreV1().Pods(upstreamNamespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, ""
			}
			return false, err.Error()
		}
		return false, "expected the pod to be deleted"
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
