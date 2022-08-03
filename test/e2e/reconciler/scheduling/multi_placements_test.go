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

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMultiPlacement(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	locationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kubeClusterClient, err := kubernetes.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclient.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, userClusterName), metav1.ListOptions{})
	require.Error(t, err)

	firstSyncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", locationClusterName)
	firstSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithSyncTarget(locationClusterName, firstSyncTargetName),
		framework.WithExtraResources("services"),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	secondSyncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", locationClusterName)
	secondSyncerFixture := framework.NewSyncerFixture(t, source, locationClusterName,
		framework.WithExtraResources("services"),
		framework.WithSyncTarget(locationClusterName, secondSyncTargetName),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Log("Label synctarget")
	patchData1 := `{"metadata":{"labels":{"loc":"loc1"}}}`
	_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, locationClusterName), firstSyncTargetName, types.MergePatchType, []byte(patchData1), metav1.PatchOptions{})
	require.NoError(t, err)
	patchData2 := `{"metadata":{"labels":{"loc":"loc2"}}}`
	_, err = kcpClusterClient.WorkloadV1alpha1().SyncTargets().Patch(logicalcluster.WithCluster(ctx, locationClusterName), secondSyncTargetName, types.MergePatchType, []byte(patchData2), metav1.PatchOptions{})
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
	_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, locationClusterName), loc1, metav1.CreateOptions{})
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
	_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, locationClusterName), loc2, metav1.CreateOptions{})
	require.NoError(t, err)

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubernetes",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       locationClusterName.String(),
					ExportName: "kubernetes",
				},
			},
		},
	}

	t.Logf("Create a binding in the user workspace")
	_, err = kcpClusterClient.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, userClusterName), binding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, userClusterName), binding.Name, metav1.GetOptions{})
		require.NoError(t, err)

		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("create two placements")
	p1 := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p1",
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{
				MatchLabels: map[string]string{"loc": "loc1"},
			}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: locationClusterName.String(),
		},
	}
	_, err = kcpClusterClient.SchedulingV1alpha1().Placements().Create(logicalcluster.WithCluster(ctx, userClusterName), p1, metav1.CreateOptions{})
	require.NoError(t, err)

	p2 := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p2",
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{
				MatchLabels: map[string]string{"loc": "loc2"},
			}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: locationClusterName.String(),
		},
	}
	_, err = kcpClusterClient.SchedulingV1alpha1().Placements().Create(logicalcluster.WithCluster(ctx, userClusterName), p2, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("disable default placement")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.SchedulingV1alpha1().Placements().Get(logicalcluster.WithCluster(ctx, userClusterName), "default", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get placement %v", err)
		}

		placement.Spec.NamespaceSelector = nil
		_, err = kcpClusterClient.SchedulingV1alpha1().Placements().Update(logicalcluster.WithCluster(ctx, userClusterName), placement, metav1.UpdateOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to update placement: %v", err)
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, userClusterName), metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.CoreV1().Services("default").Create(logicalcluster.WithCluster(ctx, userClusterName), &corev1.Service{
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
		svc, err := kubeClusterClient.CoreV1().Services("default").Get(logicalcluster.WithCluster(ctx, userClusterName), "first", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Failed to get service: %v", err)
		}

		if svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+workloadv1alpha1.ToSyncTargetKey(firstSyncerFixture.SyncerConfig.SyncTargetWorkspace, firstSyncTargetName)] != string(workloadv1alpha1.ResourceStateSync) {
			return false, fmt.Sprintf("%s is not added to ns annotation", firstSyncTargetName)
		}

		if svc.Labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+workloadv1alpha1.ToSyncTargetKey(secondSyncerFixture.SyncerConfig.SyncTargetWorkspace, secondSyncTargetName)] != string(workloadv1alpha1.ResourceStateSync) {
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
