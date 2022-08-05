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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestScheduling(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	negotiationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	secondUserClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kubeClusterClient, err := kubernetes.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclient.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, userClusterName), metav1.ListOptions{})
	require.Error(t, err)

	t.Logf("Check that there is no services resource in the second user workspace")
	_, err = kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, secondUserClusterName), metav1.ListOptions{})
	require.Error(t, err)

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", negotiationClusterName)
	syncerFixture := framework.NewSyncerFixture(t, source, negotiationClusterName,
		framework.WithExtraResources("services"),
		framework.WithSyncTarget(negotiationClusterName, syncTargetName),
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

	t.Logf("Wait for APIResourceImports to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		imports, err := kcpClusterClient.ApiresourceV1alpha1().APIResourceImports().List(logicalcluster.WithCluster(ctx, negotiationClusterName), metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceImports: %v", err)
			return false
		}

		return len(imports.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for NegotiatedAPIResources to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.ApiresourceV1alpha1().NegotiatedAPIResources().List(logicalcluster.WithCluster(ctx, negotiationClusterName), metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list NegotiatedAPIResources: %v", err)
			return false
		}

		return len(resources.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apiexport")
	var export *apisv1alpha1.APIExport
	require.Eventually(t, func() bool {
		export, err = kcpClusterClient.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, negotiationClusterName), "kubernetes", metav1.GetOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apibinding that is bound")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, negotiationClusterName), "kubernetes", metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return false, ""
		}
		return binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound, toYaml(binding)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		schemas, err := kcpClusterClient.ApisV1alpha1().APIResourceSchemas().List(logicalcluster.WithCluster(ctx, negotiationClusterName), metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceSchemas: %v", err)
			return false
		}

		return len(schemas.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the APIExport spec")
	require.Eventually(t, func() bool {
		export, err := kcpClusterClient.ApisV1alpha1().APIExports().Get(logicalcluster.WithCluster(ctx, negotiationClusterName), export.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return len(export.Spec.LatestResourceSchemas) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a location in the negotiation workspace")
	location := &schedulingv1alpha1.Location{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "us-east1",
			Labels: map[string]string{"foo": "42"},
		},
		Spec: schedulingv1alpha1.LocationSpec{
			Resource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
		},
	}
	_, err = kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, negotiationClusterName), location, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for available instances in the location")
	framework.Eventually(t, func() (bool, string) {
		location, err := kcpClusterClient.SchedulingV1alpha1().Locations().Get(logicalcluster.WithCluster(ctx, negotiationClusterName), location.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return location.Status.AvailableInstances != nil && *location.Status.AvailableInstances == 1, fmt.Sprintf("instances in status not updated:\n%s", toYaml(location))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kubernetes",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       negotiationClusterName.String(),
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
		if err != nil {
			return false, fmt.Sprintf("failed to list Locations: %v", err)
		}
		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for placement to be ready")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.SchedulingV1alpha1().Placements().Get(logicalcluster.WithCluster(ctx, userClusterName), "default", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get placement: %v", err)
		}

		return conditions.IsTrue(placement, schedulingv1alpha1.PlacementReady), fmt.Sprintf("placement is not ready: %s", toYaml(binding))
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

	t.Logf("Create a binding in the second user workspace")
	_, err = kcpClusterClient.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, secondUserClusterName), binding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.ApisV1alpha1().APIBindings().Get(logicalcluster.WithCluster(ctx, secondUserClusterName), binding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list Locations: %v", err)
		}

		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for placement to be ready")
	framework.Eventually(t, func() (bool, string) {
		placement, err := kcpClusterClient.SchedulingV1alpha1().Placements().Get(logicalcluster.WithCluster(ctx, secondUserClusterName), "default", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get placement: %v", err)
		}

		return conditions.IsTrue(placement, schedulingv1alpha1.PlacementReady), fmt.Sprintf("placement is not ready: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.CoreV1().Services("").List(logicalcluster.WithCluster(ctx, secondUserClusterName), metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncerFixture.SyncerConfig.SyncTargetWorkspace, syncTargetName)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.CoreV1().Services("default").Create(logicalcluster.WithCluster(ctx, userClusterName), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"state.workload.kcp.dev/" + syncTargetKey: "Sync",
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
	_, err = kubeClusterClient.CoreV1().Services("default").Create(logicalcluster.WithCluster(ctx, secondUserClusterName), &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "second",
			Labels: map[string]string{
				"state.workload.kcp.dev/" + syncTargetKey: "Sync",
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
			LabelSelector: "internal.workload.kcp.dev/cluster=" + syncTargetKey,
		})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
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

	names := sets.NewString()
	for _, downstreamService := range downstreamServices.Items {
		names.Insert(downstreamService.Name)
	}
	require.Equal(t, names.List(), []string{"first", "second"})

	t.Logf("Wait for placement annotation on the default namespace")
	framework.Eventually(t, func() (bool, string) {
		ns, err := kubeClusterClient.CoreV1().Namespaces().Get(logicalcluster.WithCluster(ctx, userClusterName), "default", metav1.GetOptions{})
		require.NoError(t, err)

		_, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
		return found, fmt.Sprintf("no %s annotation:\n%s", schedulingv1alpha1.PlacementAnnotationKey, toYaml(ns))
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

func toYaml(obj interface{}) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}
