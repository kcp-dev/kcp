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
	"math/rand"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPlacementUpdate(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	locationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	firstSyncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", locationClusterName)
	syncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(firstSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
		framework.WithExtraResources("services"),
	).Start(t)

	t.Log("Wait for \"default\" location")
	require.Eventually(t, func() bool {
		_, err = kcpClusterClient.Cluster(locationClusterName.Path()).SchedulingV1alpha1().Locations().Get(ctx, "default", metav1.GetOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	placementName := "placement-test-update"
	t.Logf("Bind user workspace to location workspace")
	framework.NewBindCompute(t, userClusterName.Path(), source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationClusterName.Path()),
		framework.WithPlacementNameBindOption(placementName),
	).Bind(t)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	firstSyncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncerFixture.SyncerConfig.SyncTargetClusterName, firstSyncTargetName)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"test.workload.kcp.dev": firstSyncTargetName,
			},
			Annotations: map[string]string{
				"finalizers.workload.kcp.dev/" + firstSyncTargetKey: "wait-a-bit",
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

	t.Logf("Wait for the service to have the sync label")
	framework.Eventually(t, func() (bool, string) {
		svc, err := kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Get(ctx, "first", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get service: %v", err)
		}

		return svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+firstSyncTargetKey] == string(workloadv1alpha1.ResourceStateSync), ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for the service to be sync to the downstream cluster")
	var downstreamServices *corev1.ServiceList
	framework.Eventually(t, func() (bool, string) {
		downstreamServices, err = syncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "test.workload.kcp.dev=" + firstSyncTargetName,
		})

		if err != nil {
			return false, fmt.Sprintf("Failed to list service: %v", err)
		}

		if len(downstreamServices.Items) < 1 {
			return false, "service is not synced"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Update placement to disable scheduling on the ns")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		placement.Spec.NamespaceSelector = nil
		_, err = kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Update(ctx, placement, metav1.UpdateOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to update placement: %v", err)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Placement should turn to unbound phase")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		return placement.Status.Phase == schedulingv1alpha1.PlacementUnbound, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	framework.Eventually(t, func() (bool, string) {
		ns, err := kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get ns: %v", err)
		}

		if len(ns.Annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey]) == 0 {
			return false, fmt.Sprintf("namespace should have a %s annotation, but it does not", workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	framework.Eventually(t, func() (bool, string) {
		svc, err := kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Get(ctx, "first", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get service: %v", err)
		}

		if len(svc.Annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey]) == 0 {
			return false, fmt.Sprintf("service should have a %s annotation, but it does not", workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Remove the soft finalizer on the service")
	_, err = kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Patch(ctx, "first", types.MergePatchType,
		[]byte("{\"metadata\":{\"annotations\":{\"finalizers.workload.kcp.dev/"+firstSyncTargetKey+"\":\"\"}}}"), metav1.PatchOptions{})
	require.NoError(t, err)

	t.Logf("Wait for the service to be removed in the downstream cluster")
	require.Eventually(t, func() bool {
		downstreamServices, err = syncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "test.workload.kcp.dev=" + firstSyncTargetName,
		})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		} else if len(downstreamServices.Items) != 0 {
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		placement.Spec.LocationSelectors = []metav1.LabelSelector{
			{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
		}
		_, err = kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Update(ctx, placement, metav1.UpdateOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to update placement: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Placement should turn to pending phase")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, placementName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		return placement.Status.Phase == schedulingv1alpha1.PlacementPending, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create a new placement to include the location")
	newPlacement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "new-placement",
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: locationClusterName.String(),
		},
	}
	_, err = kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Create(ctx, newPlacement, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for new placement to be ready")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.Cluster(userClusterName.Path()).SchedulingV1alpha1().Placements().Get(ctx, newPlacement.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get placement: %v", err)
		}

		condition := conditions.Get(placement, schedulingv1alpha1.PlacementReady)
		if condition == nil {
			return false, fmt.Sprintf("no %s condition exists", schedulingv1alpha1.PlacementReady)
		}
		if condition.Status == corev1.ConditionTrue {
			return true, ""
		}
		return false, fmt.Sprintf("not done waiting for the placement to be ready, reason: %v - message: %v", condition.Reason, condition.Message)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for resource to by synced again")
	framework.Eventually(t, func() (bool, string) {
		svc, err := kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Get(ctx, "first", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get service: %v", err)
		}

		if len(svc.Annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey]) != 0 {
			return false, fmt.Sprintf("resource should not have the %s annotation, but have %s", workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey, svc.Annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+firstSyncTargetKey])
		}
		return svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+firstSyncTargetKey] == string(workloadv1alpha1.ResourceStateSync), ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for the service to be sync to the downstream cluster")
	require.Eventually(t, func() bool {
		downstreamServices, err = syncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "test.workload.kcp.dev=" + firstSyncTargetName,
		})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Logf("Failed to list Services: %v", err)
			return false
		} else if len(downstreamServices.Items) < 1 {
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
