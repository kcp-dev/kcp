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

package syncer

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	kyaml "sigs.k8s.io/yaml"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestSyncerLifecycle(t *testing.T) {
	t.Parallel()

	upstreamServer := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, upstreamServer)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, upstreamServer, orgClusterName)

	// The Start method of the fixture will initiate syncer start and then wait for
	// its sync target to go ready. This implicitly validates the syncer
	// heartbeating and the heartbeat controller setting the sync target ready in
	// response.
	syncerFixture := framework.NewSyncerFixture(t, upstreamServer, wsClusterName).Start(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamConfig := upstreamServer.BaseConfig(t)
	upstreamKubeClusterClient, err := kubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	t.Log("Creating upstream namespace...")
	upstreamNamespace, err := upstreamKubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, wsClusterName), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-syncer",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	downstreamKubeClient, err := kubernetesclientset.NewForConfig(syncerFixture.DownstreamConfig)
	require.NoError(t, err)

	upstreamKcpClient, err := kcpclientset.NewForConfig(syncerFixture.SyncerConfig.UpstreamConfig)
	require.NoError(t, err)

	syncTarget, err := upstreamKcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx,
		syncerFixture.SyncerConfig.SyncTargetName,
		metav1.GetOptions{},
	)
	require.NoError(t, err)

	desiredNSLocator := shared.NewNamespaceLocator(wsClusterName, logicalcluster.From(syncTarget),
		syncTarget.GetUID(), syncTarget.Name, upstreamNamespace.Name)
	downstreamNamespaceName, err := shared.PhysicalClusterNamespaceName(desiredNSLocator)
	require.NoError(t, err)

	t.Logf("Waiting for downstream namespace to be created...")
	require.Eventually(t, func() bool {
		_, err = downstreamKubeClient.CoreV1().Namespaces().Get(ctx, downstreamNamespaceName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false
			}
			require.NoError(t, err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream namespace %s for upstream namespace %s was not created", downstreamNamespaceName, upstreamNamespace.Name)

	configMapName := "kcp-root-ca.crt"
	t.Logf("Waiting for downstream configmap %s/%s to be created...", downstreamNamespaceName, configMapName)
	require.Eventually(t, func() bool {
		_, err = downstreamKubeClient.CoreV1().ConfigMaps(downstreamNamespaceName).Get(ctx, configMapName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("saw an error waiting for downstream configmap %s/%s to be created: %v", downstreamNamespaceName, configMapName, err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream configmap %s/%s was not created", downstreamNamespaceName, configMapName)

	t.Log("Creating upstream deployment...")

	deploymentYAML, err := embeddedResources.ReadFile("deployment.yaml")
	require.NoError(t, err, "failed to read embedded deployment")

	var deployment *appsv1.Deployment
	err = yaml.Unmarshal(deploymentYAML, &deployment)
	require.NoError(t, err, "failed to unmarshal deployment")

	// This test created a new workspace that initially lacked support for deployments, but once the
	// sync target went ready (checked by the syncer fixture's Start method) the api importer
	// will have enabled deployments in the logical cluster.
	upstreamDeployment, err := upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Create(logicalcluster.WithCluster(ctx, wsClusterName), deployment, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create deployment")

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.Name)

	t.Logf("Waiting for upstream deployment %s/%s to get the syncer finalizer", upstreamNamespace.Name, upstreamDeployment.Name)
	require.Eventually(t, func() bool {
		deployment, err = upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Get(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("saw an error waiting for upstream deployment %s/%s to get the syncer finalizer: %v", upstreamNamespace.Name, upstreamDeployment.Name, err)
		}
		for _, finalizer := range deployment.Finalizers {
			if finalizer == "workload.kcp.dev/syncer-"+syncTargetKey {
				return true
			}
		}
		return false
	}, wait.ForeverTestTimeout, time.Millisecond*100, "Upstream deployment %s/%s syncer finalizer was not added", upstreamNamespace.Name, upstreamDeployment.Name)

	t.Logf("Waiting for downstream deployment %s/%s to be created...", downstreamNamespaceName, upstreamDeployment.Name)
	require.Eventually(t, func() bool {
		deployment, err = downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("saw an error waiting for downstream deployment %s/%s to be created: %v", downstreamNamespaceName, upstreamDeployment.Name, err)
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream deployment %s/%s was not synced", downstreamNamespaceName, upstreamDeployment.Name)

	if len(framework.TestConfig.PClusterKubeconfig()) > 0 {
		t.Logf("Check for available replicas if downstream is capable of actually running the deployment")
		expectedAvailableReplicas := int32(1)
		var lastEvents time.Time
		framework.Eventually(t, func() (bool, string) {
			deployment, err = downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, upstreamDeployment.Name, metav1.GetOptions{})
			require.NoError(t, err)
			if expectedAvailableReplicas == deployment.Status.AvailableReplicas {
				return true, ""
			}
			lastEvents = dumpPodEvents(t, lastEvents, downstreamKubeClient, downstreamNamespaceName)
			return false, toYaml(deployment)
		}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream deployment %s/%s didn't get available", downstreamNamespaceName, upstreamDeployment.Name)

		// This test creates a deployment upstream, and will run downstream with a mutated projected
		// in-cluster kubernetes config that points back to KCP. The running container will use this config to
		// create a configmap upstream to verify the correctness of the mutation.
		t.Logf("Create upstream service account permissions for downstream in-cluster configuration test")

		configmapAdminRoleYAML, err := embeddedResources.ReadFile("configmap-admin-role.yaml")
		require.NoError(t, err, "failed to read embedded role")

		var configmapAdminRole *rbacv1.Role
		err = yaml.Unmarshal(configmapAdminRoleYAML, &configmapAdminRole)
		require.NoError(t, err, "failed to unmarshal role")

		_, err = upstreamKubeClusterClient.RbacV1().Roles(upstreamNamespace.Name).Create(logicalcluster.WithCluster(ctx, wsClusterName), configmapAdminRole, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create upstream role")

		configmapAdminRoleBindingYAML, err := embeddedResources.ReadFile("configmap-admin-rolebinding.yaml")
		require.NoError(t, err, "failed to read embedded rolebinding")

		var configmapAdminRoleBinding *rbacv1.RoleBinding
		err = yaml.Unmarshal(configmapAdminRoleBindingYAML, &configmapAdminRoleBinding)
		require.NoError(t, err, "failed to unmarshal rolebinding")

		_, err = upstreamKubeClusterClient.RbacV1().RoleBindings(upstreamNamespace.Name).Create(logicalcluster.WithCluster(ctx, wsClusterName), configmapAdminRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create upstream rolebinding")

		t.Logf("Creating upstream in-cluster configuration test deployment")

		iccDeploymentYAML, err := embeddedResources.ReadFile("in-cluster-config-test-deployment.yaml")
		require.NoError(t, err, "failed to read embedded deployment")

		var iccDeployment *appsv1.Deployment
		err = yaml.Unmarshal(iccDeploymentYAML, &iccDeployment)
		require.NoError(t, err, "failed to unmarshal deployment")
		iccDeployment.Spec.Template.Spec.Containers[0].Image = framework.TestConfig.KCPTestImage()
		expectedConfigMapName := "expected-configmap"
		iccDeployment.Spec.Template.Spec.Containers[0].Env[0].Value = expectedConfigMapName

		iccUpstreamDeployment, err := upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Create(logicalcluster.WithCluster(ctx, wsClusterName), iccDeployment, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create icc-test deployment")

		t.Logf("Waiting for downstream in-cluster config test deployment %s/%s to be created...", downstreamNamespaceName, iccUpstreamDeployment.Name)
		var logState map[string]*metav1.Time
		framework.Eventually(t, func() (bool, string) {
			deployment, err = downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, iccUpstreamDeployment.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, ""
			}
			require.NoError(t, err)
			dumpPodEvents(t, lastEvents, downstreamKubeClient, downstreamNamespaceName)
			logState = dumpPodLogs(t, logState, downstreamKubeClient, downstreamNamespaceName)
			return deployment.Status.AvailableReplicas == 1, toYaml(deployment)
		}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream deployment %s/%s was not synced", downstreamNamespaceName, iccUpstreamDeployment.Name)

		t.Logf("Waiting for configmap generated by icc-test deployment to show up upstream")
		require.Eventually(t, func() bool {
			logState = dumpPodLogs(t, logState, downstreamKubeClient, downstreamNamespaceName)

			_, err := upstreamKubeClusterClient.CoreV1().ConfigMaps(upstreamNamespace.Name).Get(logicalcluster.WithCluster(ctx, wsClusterName), expectedConfigMapName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false
			}
			require.NoError(t, err)
			return true
		}, wait.ForeverTestTimeout, time.Millisecond*100, "upstream configmap %s/%s was not found", upstreamNamespace.Name, expectedConfigMapName)

	}
	// Delete the deployment
	err = downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Delete(ctx, deployment.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	// Wait the deployment to be recreated and check it is a different UID
	t.Logf("Waiting for downstream deployment %s/%s to be created...", downstreamNamespaceName, upstreamDeployment.Name)
	require.Eventually(t, func() bool {
		newDeployment, err := downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		require.NoError(t, err)
		return deployment.UID != newDeployment.UID
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream deployment %s/%s was not synced", downstreamNamespaceName, upstreamDeployment.Name)

	// Add a virtual Finalizer to the deployment and update it.
	t.Logf("Adding a virtual finalizer to the upstream deployment %s/%s in order to simulate an external controller", upstreamNamespace.Name, upstreamDeployment.Name)
	deploymentPatch := []byte(`{"metadata":{"annotations":{"finalizers.workload.kcp.dev/` + syncTargetKey + `":"external-controller-finalizer"}}}`)
	_, err = upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Patch(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, types.MergePatchType, deploymentPatch, metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Deleting upstream deployment %s/%s", upstreamNamespace.Name, upstreamDeployment.Name)
	err = upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Delete(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, metav1.DeleteOptions{GracePeriodSeconds: pointer.Int64(0)})
	require.NoError(t, err)

	t.Logf("Checking if the upstream deployment %s/%s has the per-location deletion annotation set", upstreamNamespace.Name, upstreamDeployment.Name)
	framework.Eventually(t, func() (bool, string) {
		deployment, err := upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Get(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, ""
		}
		require.NoError(t, err)
		if val, ok := deployment.GetAnnotations()["deletion.internal.workload.kcp.dev/"+syncTargetKey]; ok && val != "" {
			return true, ""
		}
		return false, toYaml(deployment)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "upstream Deployment %s/%s didn't get the per-location deletion annotation set or there was an error", upstreamNamespace.Name, upstreamDeployment.Name)

	t.Logf("Checking if upstream deployment %s/%s is getting deleted, shouldn't as the syncer will not remove its finalizer due to the virtual finalizer", upstreamNamespace.Name, upstreamDeployment.Name)
	require.Never(t, func() bool {
		_, err := upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Get(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true
		}
		require.NoError(t, err)
		return false
	}, 5*time.Second, time.Second, "upstream Deployment %s/%s got deleted or there was an error", upstreamNamespace.Name, upstreamDeployment.Name)

	t.Logf("Checking if the downstream deployment %s/%s is deleted or not (shouldn't as there's a virtual finalizer that blocks the deletion of the downstream resource)", downstreamNamespaceName, upstreamDeployment.Name)
	require.Neverf(t, func() bool {
		_, err := downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true
		}
		require.NoError(t, err)
		return false
	}, 5*time.Second, time.Second, "downstream Deployment %s/%s got deleted or there was an error", downstreamNamespaceName, upstreamDeployment.Name)

	// deleting a virtual Finalizer on the deployment and updating it.
	t.Logf("Removing the virtual finalizer on the upstream deployment %s/%s, the deployment deletion should go through after this", upstreamNamespace.Name, upstreamDeployment.Name)
	deploymentPatch = []byte(`{"metadata":{"annotations":{"finalizers.workload.kcp.dev/` + syncTargetKey + `": null}}}`)
	_, err = upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Patch(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, types.MergePatchType, deploymentPatch, metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for upstream deployment %s/%s to be deleted", upstreamNamespace.Name, upstreamDeployment.Name)
	require.Eventually(t, func() bool {
		_, err := upstreamKubeClusterClient.AppsV1().Deployments(upstreamNamespace.Name).Get(logicalcluster.WithCluster(ctx, wsClusterName), upstreamDeployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true
		}
		require.NoError(t, err)
		return false
	}, wait.ForeverTestTimeout, time.Millisecond*100, "upstream Deployment %s/%s was not deleted", upstreamNamespace.Name, upstreamDeployment.Name)

}

func dumpPodEvents(t *testing.T, startAfter time.Time, downstreamKubeClient *kubernetesclientset.Clientset, downstreamNamespaceName string) time.Time {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	eventList, err := downstreamKubeClient.CoreV1().Events(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Error getting events: %v", err)
		return startAfter // ignore. Error here are not the ones we care for.
	}

	sort.Slice(eventList.Items, func(i, j int) bool {
		return eventList.Items[i].LastTimestamp.Time.Before(eventList.Items[j].LastTimestamp.Time)
	})

	last := startAfter
	for _, event := range eventList.Items {
		if event.InvolvedObject.Kind != "Pod" {
			continue
		}
		if event.LastTimestamp.After(startAfter) {
			t.Logf("Event for pod %s/%s: %s", event.InvolvedObject.Namespace, event.InvolvedObject.Name, event.Message)
		}
		if event.LastTimestamp.After(last) {
			last = event.LastTimestamp.Time
		}
	}

	pods, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Error getting pods: %v", err)
		return last // ignore. Error here are not the ones we care for.
	}

	for _, pod := range pods.Items {
		for _, s := range pod.Status.ContainerStatuses {
			if s.State.Terminated != nil && s.State.Terminated.FinishedAt.After(startAfter) {
				t.Logf("Pod %s/%s container %s terminated with exit code %d: %s", pod.Namespace, pod.Name, s.Name, s.State.Terminated.ExitCode, s.State.Terminated.Message)
			}
		}
	}

	return last
}

func dumpPodLogs(t *testing.T, startAfter map[string]*metav1.Time, downstreamKubeClient *kubernetesclientset.Clientset, downstreamNamespaceName string) map[string]*metav1.Time {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	if startAfter == nil {
		startAfter = make(map[string]*metav1.Time)
	}

	pods, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("Error getting pods: %v", err)
		return startAfter // ignore. Error here are not the ones we care for.
	}
	for _, pod := range pods.Items {
		for _, c := range pod.Spec.Containers {
			key := fmt.Sprintf("%s/%s", pod.Name, c.Name)
			now := metav1.Now()
			res, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespaceName).GetLogs(pod.Name, &corev1.PodLogOptions{
				SinceTime: startAfter[key],
				Container: c.Name,
			}).DoRaw(ctx)
			if err != nil {
				klog.Errorf("Failed to get logs for pod %s/%s container %s: %v", pod.Namespace, pod.Name, c.Name, err)
				continue
			}
			for _, line := range strings.Split(string(res), "\n") {
				t.Logf("Pod %s/%s container %s: %s", pod.Namespace, pod.Name, c.Name, line)
			}
			startAfter[key] = &now
		}
	}

	return startAfter
}

func toYaml(obj interface{}) string {
	b, err := kyaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestSyncWorkload(t *testing.T) {
	t.Parallel()

	syncTargetName := "test-wlc"
	upstreamServer := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, upstreamServer)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, upstreamServer, orgClusterName)

	// Write the upstream logical cluster config to disk for the workspace plugin
	upstreamRawConfig, err := upstreamServer.RawConfig()
	require.NoError(t, err)
	_, kubeconfigPath := framework.WriteLogicalClusterConfig(t, upstreamRawConfig, "base", wsClusterName)

	subCommand := []string{
		"workload",
		"sync",
		syncTargetName,
		"--syncer-image",
		"ghcr.io/kcp-dev/kcp/syncer-c2e3073d5026a8f7f2c47a50c16bdbec:41ca72b",
		"--output-file", "-",
	}

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommand)

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommand)

}

func TestCordonUncordonDrain(t *testing.T) {
	t.Parallel()

	upstreamServer := framework.SharedKcpServer(t)

	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, upstreamServer)

	upstreamCfg := upstreamServer.BaseConfig(t)

	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, upstreamServer, orgClusterName)

	// Write the upstream logical cluster config to disk for the workspace plugin
	upstreamRawConfig, err := upstreamServer.RawConfig()
	require.NoError(t, err)
	_, kubeconfigPath := framework.WriteLogicalClusterConfig(t, upstreamRawConfig, "base", wsClusterName)

	kcpClusterClient, err := kcpclientset.NewForConfig(upstreamCfg)
	require.NoError(t, err, "failed to construct client for server")

	// The Start method of the fixture will initiate syncer start and then wait for
	// its sync target to go ready. This implicitly validates the syncer
	// heartbeating and the heartbeat controller setting the sync target ready in
	// response.
	syncerFixture := framework.NewSyncerFixture(t, upstreamServer, wsClusterName).Start(t)
	syncTargetName := syncerFixture.SyncerConfig.SyncTargetName

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	t.Log("Check initial workload")
	cluster, err := kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, wsClusterName), syncTargetName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get sync target", syncTargetName)
	require.False(t, cluster.Spec.Unschedulable)
	require.Nil(t, cluster.Spec.EvictAfter)

	t.Log("Cordon workload")
	subCommandCordon := []string{
		"workload",
		"cordon",
		syncTargetName,
	}
	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandCordon)

	t.Log("Check workload after cordon")
	cluster, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, wsClusterName), syncTargetName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get sync target", syncTargetName)
	require.True(t, cluster.Spec.Unschedulable)
	require.Nil(t, cluster.Spec.EvictAfter)

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandCordon)

	t.Log("Uncordon workload")
	subCommandUncordon := []string{
		"workload",
		"uncordon",
		syncTargetName,
	}
	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandUncordon)

	t.Log("Check workload after uncordon")
	cluster, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, wsClusterName), syncTargetName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get sync target", syncTargetName)
	require.False(t, cluster.Spec.Unschedulable)
	require.Nil(t, cluster.Spec.EvictAfter)

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandUncordon)

	t.Log("Drain workload")
	subCommandDrain := []string{
		"workload",
		"drain",
		syncTargetName,
	}
	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandDrain)

	t.Log("Check workload after drain started")
	cluster, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, wsClusterName), syncTargetName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get sync target", syncTargetName)
	require.True(t, cluster.Spec.Unschedulable)
	require.NotNil(t, cluster.Spec.EvictAfter)

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandDrain)

	t.Log("Remove drain, uncordon workload")
	subCommandUncordon = []string{
		"workload",
		"uncordon",
		syncTargetName,
	}

	framework.RunKcpCliPlugin(t, kubeconfigPath, subCommandUncordon)

	t.Log("Check workload after uncordon")
	cluster, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, wsClusterName), syncTargetName, metav1.GetOptions{})
	require.NoError(t, err, "failed to get sync target", syncTargetName)
	require.False(t, cluster.Spec.Unschedulable)
	require.Nil(t, cluster.Spec.EvictAfter)

}
