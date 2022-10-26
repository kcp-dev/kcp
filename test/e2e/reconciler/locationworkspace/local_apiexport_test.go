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

	"github.com/google/go-cmp/cmp"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgodiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestSyncTargetLocalExport is to test that user import kubernetes API in synctarget workspace and use it,
// instead of using global kubernetes APIExport.
// TODO(qiujian16) This might be removed when we do not support local export later.
func TestSyncTargetLocalExport(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClients, err := clientset.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncTarget := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithAPIExports(""),
		framework.WithExtraResources("services"),
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
			)
			require.NoError(t, err)
		}),
	).Start(t)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 1 {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "services" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// create virtual workspace rest configs
	rawConfig, err := source.RawConfig()
	require.NoError(t, err)
	virtualWorkspaceRawConfig := rawConfig.DeepCopy()
	virtualWorkspaceRawConfig.Clusters["syncvervw"] = rawConfig.Clusters["base"].DeepCopy()
	virtualWorkspaceRawConfig.Clusters["syncvervw"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + computeClusterName.String() + "/" + syncTargetName + "/" + syncTarget.SyncerConfig.SyncTargetUID
	virtualWorkspaceRawConfig.Contexts["syncvervw"] = rawConfig.Contexts["base"].DeepCopy()
	virtualWorkspaceRawConfig.Contexts["syncvervw"].Cluster = "syncvervw"
	virtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "syncvervw", nil, nil).ClientConfig()
	require.NoError(t, err)
	virtualWorkspaceConfig = kcpclienthelper.SetMultiClusterRoundTripper(rest.AddUserAgent(rest.CopyConfig(virtualWorkspaceConfig), t.Name()))

	virtualWorkspaceiscoverClusterClient, err := clientgodiscovery.NewDiscoveryClientForConfig(virtualWorkspaceConfig)
	require.NoError(t, err)

	t.Logf("Wait for service API from synctarget workspace to be served in synctarget virtual workspace.")
	require.Eventually(t, func() bool {
		_, existingAPIResourceLists, err := virtualWorkspaceiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
		if err != nil {
			return false
		}
		// requiredAPIResourceList includes all core APIs plus services API
		return len(cmp.Diff([]*metav1.APIResourceList{
			requiredAPIResourceListWithService(computeClusterName, computeClusterName)}, sortAPIResourceList(existingAPIResourceLists))) == 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should be authorized to access downstream clusters")
	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		return conditions.IsTrue(syncTarget, workloadv1alpha1.SyncerAuthorized)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(computeClusterName).CoreV1().Services("default").List(ctx, metav1.ListOptions{})
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
	_, err = kubeClusterClient.Cluster(computeClusterName).CoreV1().Services("default").Create(ctx, &corev1.Service{
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
		downstreamServices, err := syncTarget.DownstreamKubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{
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
