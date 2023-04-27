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

package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestScheduling(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, source, framework.TODO_WithoutMultiShardSupport())
	negotiationPath, _ := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	userPath, userWorkspace := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	secondUserPath, secondUserWorkspace := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.Cluster(userPath).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	t.Logf("Check that there is no services resource in the second user workspace")
	_, err = kubeClusterClient.Cluster(secondUserPath).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", negotiationPath)
	syncerFixture := framework.NewSyncerFixture(t, source, negotiationPath,
		framework.WithSyncTargetName(syncTargetName),
		framework.WithSyncedUserWorkspaces(userWorkspace, secondUserWorkspace),
	).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)

	t.Logf("Wait for APIResourceImports to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		imports, err := kcpClusterClient.Cluster(negotiationPath).ApiresourceV1alpha1().APIResourceImports().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("Failed to list APIResourceImports: %v", err)
			return false
		}

		return len(imports.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for NegotiatedAPIResources to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.Cluster(negotiationPath).ApiresourceV1alpha1().NegotiatedAPIResources().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("Failed to list NegotiatedAPIResources: %v", err)
			return false
		}

		return len(resources.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a location in the negotiation workspace")
	location := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "us-east1",
			Labels: map[string]string{"foo": "42"},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.io",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
		},
	}
	_, err = kcpClusterClient.Cluster(negotiationPath).SchedulingV1alpha1().Locations().Create(ctx, location, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for available instances in the location")
	framework.Eventually(t, func() (bool, string) {
		location, err := kcpClusterClient.Cluster(negotiationPath).SchedulingV1alpha1().Locations().Get(ctx, location.Name, metav1.GetOptions{})
		require.NoError(t, err)
		if location.Status.AvailableInstances == nil {
			return false, "location.Status.AvailableInstances not present"
		}
		if actual, expected := *location.Status.AvailableInstances, uint32(1); actual != expected {
			return false, fmt.Sprintf("location.Status.AvailableInstances is %d, not %d", actual, expected)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, userPath, source,
		framework.WithLocationWorkspaceWorkloadBindOption(negotiationPath),
	).Bind(t)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(userPath).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind second user workspace to location workspace")
	framework.NewBindCompute(t, secondUserPath, source,
		framework.WithLocationWorkspaceWorkloadBindOption(negotiationPath),
	).Bind(t)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(secondUserPath).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncerFixture.SyncTargetClusterName, syncTargetName)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.Cluster(userPath).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"state.workload.kcp.io/" + syncTargetKey: "Sync",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create a service in the second user workspace")
	_, err = kubeClusterClient.Cluster(secondUserPath).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "second",
			Labels: map[string]string{
				"state.workload.kcp.io/" + syncTargetKey: "Sync",
			},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for the 2 services to be sync to the downstream cluster")
	var downstreamServices *corev1.ServiceList
	require.Eventually(t, func() bool {
		downstreamServices, err = syncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "internal.workload.kcp.io/cluster=" + syncTargetKey,
		})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		} else if len(downstreamServices.Items) < 2 {
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	syncedServicesYaml, err := yaml.Marshal(downstreamServices)
	require.NoError(t, err)
	t.Logf("Synced services:\n%s", syncedServicesYaml)

	require.Len(t, downstreamServices.Items, 2)

	names := sets.New[string]()
	for _, downstreamService := range downstreamServices.Items {
		names.Insert(downstreamService.Name)
	}
	require.Equal(t, sets.List[string](names), []string{"first", "second"})

	t.Logf("Wait for placement annotation on the default namespace")
	framework.Eventually(t, func() (bool, string) {
		ns, err := kubeClusterClient.Cluster(userPath).CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
		require.NoError(t, err)

		_, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
		return found, fmt.Sprintf("no %s annotation:\n%s", schedulingv1alpha1.PlacementAnnotationKey, ns.Annotations)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

// TestSchedulingWhenLocationIsMissing create placement at first when location is missing and created later.
func TestSchedulingWhenLocationIsMissing(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, source, framework.TODO_WithoutMultiShardSupport())
	locationPath, _ := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	userPath, userWorkspace := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())

	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	// create a placement at first
	newPlacement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "new-placement",
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.io",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: locationPath.String(),
		},
	}
	_, err = kcpClusterClient.Cluster(userPath).SchedulingV1alpha1().Placements().Create(ctx, newPlacement, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Placement should turn to pending phase")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userPath).SchedulingV1alpha1().Placements().Get(ctx, newPlacement.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		return placement.Status.Phase == schedulingv1alpha1.PlacementPending, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", locationPath)
	_ = framework.NewSyncerFixture(t, source, locationPath,
		framework.WithSyncTargetName(syncTargetName),
		framework.WithSyncedUserWorkspaces(userWorkspace),
	).CreateSyncTargetAndApplyToDownstream(t).StartAPIImporter(t).StartHeartBeat(t)

	t.Logf("Wait for placement to be ready")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(userPath).SchedulingV1alpha1().Placements().Get(ctx, newPlacement.Name, metav1.GetOptions{})
	}, framework.Is(schedulingv1alpha1.PlacementReady))
}
