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

package locationworkspace

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRootComputeWorkspace(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClients, err := clientset.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncerFixture := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithSyncTarget(computeClusterName, syncTargetName),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
				metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Logf("Patch synctarget with new export")
	patchData := `{"spec":{"supportedAPIExports":[{"workspace":{"path":"root:compute","exportName":"kubernetes"}}]}}`
	_, err = kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 3 {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "services" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}
		if syncTarget.Status.SyncedResources[1].Resource != "ingresses" ||
			syncTarget.Status.SyncedResources[1].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the kubernetes export", consumerWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubernetes",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       "root:compute",
					ExportName: "kubernetes",
				},
			},
		},
	}

	_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	require.Eventually(t, func() bool {
		binding, err := kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), apiBinding.Name, metav1.GetOptions{})
		require.NoError(t, err)

		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("create a placement")
	p1 := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test",
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: computeClusterName.String(),
		},
	}
	_, err = kcpClients.Cluster(consumerWorkspace).SchedulingV1alpha1().Placements().Create(logicalcluster.WithCluster(ctx, consumerWorkspace), p1, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for placement to be ready")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClients.Cluster(consumerWorkspace).SchedulingV1alpha1().Placements().Get(logicalcluster.WithCluster(ctx, consumerWorkspace), "test", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get placement: %v", err)
		}

		return conditions.IsTrue(placement, schedulingv1alpha1.PlacementReady), fmt.Sprintf("placement is not ready: %v", placement)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(consumerWorkspace).CoreV1().Services("default").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			t.Logf("service err %v", err)
			return false
		} else if err != nil {
			t.Logf("service err %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.Cluster(consumerWorkspace).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"test.workload.kcp.dev": syncTargetName,
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

	t.Logf("Wait for the service to be synced to the downstream cluster")
	framework.Eventually(t, func() (bool, string) {
		downstreamServices, err := syncerFixture.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
			LabelSelector: "test.workload.kcp.dev=" + syncTargetName,
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
