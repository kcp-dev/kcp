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

package deployment

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/deployment/locations"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/deployment/workloads"
)

func createResources(t *testing.T, ctx context.Context, fs embed.FS, upstreamConfig *rest.Config, path logicalcluster.Path, transformers ...helpers.TransformFileFunc) error {
	dynamicClusterClient, err := kcpdynamic.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	client := dynamicClusterClient.Cluster(path)

	apiextensionsClusterClient, err := kcpapiextensionsclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	discoveryClient := apiextensionsClusterClient.Cluster(path).Discovery()

	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	return helpers.CreateResourcesFromFS(ctx, client, mapper, sets.NewString(), fs, transformers...)
}

func TestDeploymentCoordinator(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster:requires-kind")

	if len(framework.TestConfig.PClusterKubeconfig()) == 0 {
		t.Skip("Test requires a pcluster")
	}

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamServer := framework.SharedKcpServer(t)

	upstreamConfig := upstreamServer.BaseConfig(t)
	upstreamKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	kcpClusterClient, err := kcpclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	orgWorkspace := framework.NewOrganizationFixture(t, upstreamServer)

	locationWorkspace := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("synctargets"))

	workloadWorkspace1 := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("workload-1"))
	workloadWorkspace2 := framework.NewWorkspaceFixture(t, upstreamServer, orgWorkspace.Path(), framework.WithName("workload-2"))

	eastSyncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspace,
		framework.WithSyncTargetName("east"),
		framework.WithSyncedUserWorkspaces(workloadWorkspace1, workloadWorkspace2),
	).Start(t)

	_, err = kcpClusterClient.Cluster(locationWorkspace.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "east", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"east"}]`), metav1.PatchOptions{})
	require.NoError(t, err)

	westSyncer := framework.NewSyncerFixture(t, upstreamServer, locationWorkspace,
		framework.WithSyncTargetName("west"),
		framework.WithSyncedUserWorkspaces(workloadWorkspace1, workloadWorkspace2),
	).Start(t)

	_, err = kcpClusterClient.Cluster(locationWorkspace.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, "west", types.JSONPatchType, []byte(`[{"op":"add","path":"/metadata/labels/region","value":"west"}]`), metav1.PatchOptions{})
	require.NoError(t, err)

	eastSyncer.WaitForClusterReady(t, ctx)
	westSyncer.WaitForClusterReady(t, ctx)

	t.Logf("Create 2 locations, one for each SyncTargets")
	err = createResources(t, ctx, locations.FS, upstreamConfig, locationWorkspace.Path())
	require.NoError(t, err)

	t.Logf("Bind workload workspace 1 to location workspace for the east location")
	framework.NewBindCompute(t, workloadWorkspace1.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
			MatchLabels: map[string]string{"region": "east"},
		}),
	).Bind(t)

	t.Logf("Bind workload workspace 2 to location workspace for the east location")
	framework.NewBindCompute(t, workloadWorkspace2.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
			MatchLabels: map[string]string{"region": "east"},
		}),
	).Bind(t)

	t.Logf("Bind workload workspace 1 to location workspace for the west location")
	framework.NewBindCompute(t, workloadWorkspace1.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
			MatchLabels: map[string]string{"region": "west"},
		}),
	).Bind(t)

	t.Logf("Bind workload workspace 2 to location workspace for the west location")
	framework.NewBindCompute(t, workloadWorkspace2.Path(), upstreamServer,
		framework.WithLocationWorkspaceWorkloadBindOption(locationWorkspace.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{
			MatchLabels: map[string]string{"region": "west"},
		}),
	).Bind(t)

	t.Logf("Get the root compute APIExport Virtual Workspace URL")

	rootKubernetesAPIExport, err := kcpClusterClient.Cluster(logicalcluster.NewPath("root:compute")).ApisV1alpha1().APIExports().Get(ctx, "kubernetes", metav1.GetOptions{})
	require.NoError(t, err, "failed to retrieve Root compute kubernetes APIExport")

	require.GreaterOrEqual(t, len(rootKubernetesAPIExport.Status.VirtualWorkspaces), 1, "Root compute kubernetes APIExport should contain at least one virtual workspace URL")

	rootComputeKubernetesURL := rootKubernetesAPIExport.Status.VirtualWorkspaces[0].URL

	t.Logf("Start the Deployment controller")

	artifactDir, _, err := framework.ScratchDirs(t)
	require.NoError(t, err)

	executableName := "deployment-coordinator"
	cmd := append(framework.DirectOrGoRunCommand(executableName),
		"--kubeconfig="+upstreamServer.KubeconfigPath(),
		"--context=base",
		"--server="+rootComputeKubernetesURL,
	)

	deploymentCoordinator := framework.NewAccessory(t, artifactDir, executableName, cmd...)
	err = deploymentCoordinator.Run(t, framework.WithLogStreaming)
	require.NoError(t, err, "failed to start deployment coordinator")

	downstreamKubeClient, err := kubernetes.NewForConfig(eastSyncer.DownstreamConfig)
	require.NoError(t, err)

	type downstreamInfo struct {
		lastEventsOnEast time.Time
		lastEventsOnWest time.Time
		logStateOnEast   map[string]*metav1.Time
		logStateOnWest   map[string]*metav1.Time
		namespaceOnEast  string
		namespaceOnWest  string
	}

	dumpEventsAndPods := func(di downstreamInfo) downstreamInfo {
		di.lastEventsOnEast = dumpPodEvents(ctx, t, di.lastEventsOnEast, downstreamKubeClient, di.namespaceOnEast)
		di.lastEventsOnWest = dumpPodEvents(ctx, t, di.lastEventsOnWest, downstreamKubeClient, di.namespaceOnWest)
		di.logStateOnEast = dumpPodLogs(ctx, t, di.logStateOnEast, downstreamKubeClient, di.namespaceOnEast)
		di.logStateOnWest = dumpPodLogs(ctx, t, di.logStateOnWest, downstreamKubeClient, di.namespaceOnWest)
		return di
	}

	for _, workspace := range []struct {
		clusterName       logicalcluster.Name
		requestedReplicas int32
	}{
		{
			clusterName:       workloadWorkspace1,
			requestedReplicas: 4,
		},
		{
			clusterName:       workloadWorkspace2,
			requestedReplicas: 8,
		},
	} {
		wkspDownstreamInfo := downstreamInfo{
			namespaceOnEast: eastSyncer.DownstreamNamespaceFor(t, workspace.clusterName, "default"),
			namespaceOnWest: westSyncer.DownstreamNamespaceFor(t, workspace.clusterName, "default"),
		}

		t.Logf("Create workload in workload workspace %q, with replicas set to %d", workspace.clusterName, workspace.requestedReplicas)
		framework.Eventually(t, func() (bool, string) {
			if err := createResources(t, ctx, workloads.FS, upstreamConfig, workspace.clusterName.Path(), func(bs []byte) ([]byte, error) {
				yaml := string(bs)
				yaml = strings.Replace(yaml, "replicas: 1", fmt.Sprintf("replicas: %d", workspace.requestedReplicas), 1)
				return []byte(yaml), nil
			}); err == nil {
				return true, ""
			} else {
				return false, err.Error()
			}
		}, wait.ForeverTestTimeout, time.Millisecond*100, "should create the deployment after the deployments resource is available in workspace %q", workspace.clusterName)

		t.Logf("Wait for the workload in workspace %q to be started and available with 4 replicas", workspace.clusterName)
		func() {
			defer dumpEventsAndPods(wkspDownstreamInfo)

			framework.Eventually(t, func() (success bool, reason string) {
				deployment, err := upstreamKubeClusterClient.Cluster(workspace.clusterName.Path()).AppsV1().Deployments("default").Get(ctx, "test", metav1.GetOptions{})
				require.NoError(t, err)

				// TODO(davidfestal): the 2 checks below are necessary here to avoid the test to be flaky since for now the coordination
				// controller doesn't delay the syncing before setting the transformation annotations.
				// So it could be synced with 8 replicas on each Synctarget at start, for a very small amount of time, which might
				// seem like deployment replicas would have been spread, though in fact it is not.
				if _, exists := deployment.GetAnnotations()["experimental.spec-diff.workload.kcp.dev/"+eastSyncer.ToSyncTargetKey()]; !exists {
					return false, fmt.Sprintf("Deployment %s/%s should have been prepared for transformation by the coordinator for the east syncTarget", workspace.clusterName, "test")
				}
				if _, exists := deployment.GetAnnotations()["experimental.spec-diff.workload.kcp.dev/"+westSyncer.ToSyncTargetKey()]; !exists {
					return false, fmt.Sprintf("Deployment %s/%s should have been prepared for transformation by the coordinator for the west syncTarget", workspace.clusterName, "test")
				}

				// TODO(davidfestal): the 2 checks below are necessary here to avoid the test to be flaky since for now the coordination
				// controller doesn't delay the syncing before setting the transformation annotations.
				// So it could be synced with 8 replicas on each Synctarget at start, for a very small amount of time, which might
				// seem like deployment replicas would have been spread, though in fact it is not.
				if _, exists := deployment.GetAnnotations()["diff.syncer.internal.kcp.dev/"+eastSyncer.ToSyncTargetKey()]; !exists {
					return false, fmt.Sprintf("Status of deployment %s/%s  should have been updated by the east syncer", workspace.clusterName, "test")
				}
				if _, exists := deployment.GetAnnotations()["experimental.spec-diff.workload.kcp.dev/"+westSyncer.ToSyncTargetKey()]; !exists {
					return false, fmt.Sprintf("Status of deployment %s/%s  should have been updated by the west syncer", workspace.clusterName, "test")
				}

				if actual, expected := deployment.Status.AvailableReplicas, workspace.requestedReplicas; actual != expected {
					return false, fmt.Sprintf("Deployment %s/%s had %d available replicas, not %d", workspace.clusterName, "test", actual, expected)
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*500, "deployment %s/%s was not synced", workspace.clusterName, "test")
		}()

		t.Logf("Check that each deployment on each SyncTarget has half the number of replicas")
		downstreamDeploymentOnEastForWorkspace1, err := downstreamKubeClient.AppsV1().Deployments(wkspDownstreamInfo.namespaceOnEast).Get(ctx, "test", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, workspace.requestedReplicas/2, downstreamDeploymentOnEastForWorkspace1.Status.AvailableReplicas, "East syncer should have received half of the replicas for workspace %q workload", workspace.clusterName)

		downstreamDeploymentOnWestForWorkspace1, err := downstreamKubeClient.AppsV1().Deployments(wkspDownstreamInfo.namespaceOnWest).Get(ctx, "test", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, workspace.requestedReplicas/2, downstreamDeploymentOnWestForWorkspace1.Status.AvailableReplicas, "West syncer should have received half of the replicas for workspace %q workload", workspace.clusterName)
	}
}

func dumpPodEvents(ctx context.Context, t *testing.T, startAfter time.Time, downstreamKubeClient kubernetes.Interface, downstreamNamespaceName string) time.Time {
	eventList, err := downstreamKubeClient.CoreV1().Events(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("Error getting events: %v", err)
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
		t.Logf("Error getting pods: %v", err)
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

func dumpPodLogs(ctx context.Context, t *testing.T, startAfter map[string]*metav1.Time, downstreamKubeClient kubernetes.Interface, downstreamNamespaceName string) map[string]*metav1.Time {
	if startAfter == nil {
		startAfter = make(map[string]*metav1.Time)
	}

	pods, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Logf("Error getting pods: %v", err)
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
				t.Logf("Failed to get logs for pod %s/%s container %s: %v", pod.Namespace, pod.Name, c.Name, err)
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
