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

	kubeClusterClient, err := kubernetes.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	t.Logf("Check that there is no services resource in the second user workspace")
	_, err = kubeClusterClient.Cluster(secondUserClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	workloadClusterName := fmt.Sprintf("workloadcluster-%d", +rand.Intn(1000000))
	t.Logf("Creating a WorkloadCluster and syncer in %s", negotiationClusterName)
	syncerFixture := framework.SyncerFixture{
		ResourcesToSync:      sets.NewString("services"),
		UpstreamServer:       source,
		WorkspaceClusterName: negotiationClusterName,
		WorkloadClusterName:  workloadClusterName,
		InstallCRDs: func(config *rest.Config, isLogicalCluster bool) {
			if !isLogicalCluster {
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
		},
	}.Start(t)

	t.Logf("Wait for APIResourceImports to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		imports, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().APIResourceImports().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceImports: %v", err)
			return false
		}

		return len(imports.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for NegotiatedAPIResources to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().NegotiatedAPIResources().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list NegotiatedAPIResources: %v", err)
			return false
		}

		return len(resources.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apiexport")
	var export *apisv1alpha1.APIExport
	require.Eventually(t, func() bool {
		export, err = kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Get(ctx, "kubernetes", metav1.GetOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for \"kubernetes\" apibinding that is bound")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIBindings().Get(ctx, "kubernetes", metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return false, ""
		}
		return binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound, toYaml(binding)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		schemas, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIResourceSchemas().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceSchemas: %v", err)
			return false
		}

		return len(schemas.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the APIExport spec")
	require.Eventually(t, func() bool {
		export, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Get(ctx, export.Name, metav1.GetOptions{})
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
				Resource: "workloadclusters",
			},
		},
	}
	_, err = kcpClusterClient.Cluster(negotiationClusterName).SchedulingV1alpha1().Locations().Create(ctx, location, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for available instances in the location")
	framework.Eventually(t, func() (bool, string) {
		location, err := kcpClusterClient.Cluster(negotiationClusterName).SchedulingV1alpha1().Locations().Get(ctx, location.Name, metav1.GetOptions{})
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
	_, err = kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list Locations: %v", err)
		}
		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create a binding in the second user workspace")
	_, err = kcpClusterClient.Cluster(secondUserClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for binding to be ready")
	framework.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(secondUserClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list Locations: %v", err)
		}

		return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), fmt.Sprintf("binding not bound: %s", toYaml(binding))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(secondUserClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create a service in the user workspace")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"state.internal.workload.kcp.dev/" + workloadClusterName: "Sync",
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
	_, err = kubeClusterClient.Cluster(secondUserClusterName).CoreV1().Services("default").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "second",
			Labels: map[string]string{
				"state.internal.workload.kcp.dev/" + workloadClusterName: "Sync",
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
			LabelSelector: "internal.workload.kcp.dev/cluster=" + workloadClusterName,
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
		ns, err := kubeClusterClient.Cluster(userClusterName).CoreV1().Namespaces().Get(ctx, "default", metav1.GetOptions{})
		require.NoError(t, err)

		return ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey] != "", fmt.Sprintf("no %s annotation:\n%s", schedulingv1alpha1.PlacementAnnotationKey, toYaml(ns))
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

func toYaml(obj interface{}) string {
	b, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}
