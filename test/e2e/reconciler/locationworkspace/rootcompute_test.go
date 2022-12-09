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
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	discocache "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/scale"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRootComputeWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())
	consumerWorkspace := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())

	kcpClients, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncerFixture := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithSyncTargetName(syncTargetName),
		framework.WithSyncedUserWorkspaces(consumerWorkspace),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
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
		if syncTarget.Status.SyncedResources[2].Resource != "deployments" ||
			syncTarget.Status.SyncedResources[2].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, consumerWorkspace.Path(), source,
		framework.WithAPIExportsWorkloadBindOption("root:compute:kubernetes"),
		framework.WithLocationWorkspaceWorkloadBindOption(computeClusterName.Path()),
	).Bind(t)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Services("default").List(ctx, metav1.ListOptions{})
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
	_, err = kubeClusterClient.Cluster(consumerWorkspace.Path()).CoreV1().Services("default").Create(ctx, &corev1.Service{
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

	t.Logf("Wait for being able to list Deployments in the user workspace")
	framework.Eventually(t, func() (bool, string) {
		_, err := kubeClusterClient.Cluster(consumerWorkspace.Path()).AppsV1().Deployments("default").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("deployment err %v", err)
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	var replicas int32 = 1
	t.Logf("Create a deployment in the user workspace")
	_, err = kubeClusterClient.Cluster(consumerWorkspace.Path()).AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
			Labels: map[string]string{
				"test.workload.kcp.dev": syncTargetName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "myapp"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "myapp"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "name",
							Image: "image",
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for the deployment to be synced to the downstream cluster")
	framework.Eventually(t, func() (bool, string) {
		downstreamDeployments, err := syncerFixture.DownstreamKubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{
			LabelSelector: "test.workload.kcp.dev=" + syncTargetName,
		})

		if err != nil {
			return false, fmt.Sprintf("Failed to list deployment: %v", err)
		}

		if len(downstreamDeployments.Items) < 1 {
			return false, "deployment is not synced"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Scale the deployment in the upstream consumer workspace")
	discoverClient := kubeClusterClient.Cluster(consumerWorkspace.Path()).Discovery()
	cachedDiscovery := discocache.NewMemCacheClient(discoverClient)
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(cachedDiscovery)
	scaleKindResolver := scale.NewDiscoveryScaleKindResolver(discoverClient)
	scaleClient := scale.New(kubeClusterClient.Cluster(consumerWorkspace.Path()).AppsV1().RESTClient(), restMapper, dynamic.LegacyAPIPathResolverFunc, scaleKindResolver)
	_, err = scaleClient.Scales("default").Update(ctx, appsv1.SchemeGroupVersion.WithResource("deployments").GroupResource(), &v1.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name: "first",
		},
		Spec: v1.ScaleSpec{
			Replicas: 2,
		},
	}, metav1.UpdateOptions{})
	require.NoError(t, err, "deployment should support the scale subresource")
}
