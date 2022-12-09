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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMultiPlacement(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	locationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path(), framework.WithName("location"))
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path(), framework.WithName("user"))

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	firstSyncTargetName := "first-synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", locationClusterName)
	firstSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTargetName(firstSyncTargetName),
		framework.WithExtraResources("services"),
		framework.WithSyncedUserWorkspaces(userClusterName),
	).Start(t)

	secondSyncTargetName := "second-synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", locationClusterName)
	secondSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithExtraResources("services"),
		framework.WithSyncTargetName(secondSyncTargetName),
		framework.WithSyncedUserWorkspaces(userClusterName),
	).Start(t)

	t.Log("Label synctarget")
	patchData1 := `{"metadata":{"labels":{"loc":"loc1"}}}`
	_, err = kcpClusterClient.Cluster(locationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, firstSyncTargetName, types.MergePatchType, []byte(patchData1), metav1.PatchOptions{})
	require.NoError(t, err)
	patchData2 := `{"metadata":{"labels":{"loc":"loc2"}}}`
	_, err = kcpClusterClient.Cluster(locationClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, secondSyncTargetName, types.MergePatchType, []byte(patchData2), metav1.PatchOptions{})
	require.NoError(t, err)

	t.Log("Create locations")
	loc1 := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "loc1",
			Labels: map[string]string{"loc": "loc1"},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			InstanceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"loc": "loc1"},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(locationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, loc1, metav1.CreateOptions{})
	require.NoError(t, err)

	loc2 := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "loc2",
			Labels: map[string]string{"loc": "loc2"},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			InstanceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"loc": "loc2"},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(locationClusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, loc2, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind user workspace to location workspace with loc 1")
	framework.NewBindCompute(t, userClusterName.Path(), source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationClusterName.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{MatchLabels: map[string]string{"loc": "loc1"}}),
	).Bind(t)

	t.Logf("Bind user workspace to location workspace with loc 2")
	framework.NewBindCompute(t, userClusterName.Path(), source,
		framework.WithLocationWorkspaceWorkloadBindOption(locationClusterName.Path()),
		framework.WithLocationSelectorWorkloadBindOption(metav1.LabelSelector{MatchLabels: map[string]string{"loc": "loc2"}}),
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

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName.Path()).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"test.workload.kcp.dev": firstSyncTargetName,
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

		if svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+workloadv1alpha1.ToSyncTargetKey(firstSyncerFixture.SyncerConfig.SyncTargetClusterName, firstSyncTargetName)] != string(workloadv1alpha1.ResourceStateSync) {
			return false, fmt.Sprintf("%s is not added to ns annotation", firstSyncTargetName)
		}

		if svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+workloadv1alpha1.ToSyncTargetKey(secondSyncerFixture.SyncerConfig.SyncTargetClusterName, secondSyncTargetName)] != string(workloadv1alpha1.ResourceStateSync) {
			return false, fmt.Sprintf("%s is not added to ns annotation", secondSyncTargetName)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for the service to be sync to the downstream cluster")
	framework.Eventually(t, func() (bool, string) {
		downstreamServices, err := firstSyncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
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

	framework.Eventually(t, func() (bool, string) {
		downstreamServices, err := secondSyncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
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
}
