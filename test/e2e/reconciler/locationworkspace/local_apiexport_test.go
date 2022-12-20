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

	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
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
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path(), framework.WithName("compute"))

	kcpClients, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncTarget := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithAPIExports(""),
		framework.WithExtraResources("services"),
		framework.WithSyncTargetName(syncTargetName),
		framework.WithSyncedUserWorkspaces(computeClusterName),
	).Start(t)

	framework.Eventually(t, func() (bool, string) {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		require.NoError(t, err)

		if len(syncTarget.Status.SyncedResources) != 1 {
			return false, fmt.Sprintf("expected 1 synced resources (services), got %v\n\n%s", syncTarget.Status.SyncedResources, toYAML(t, syncTarget))
		}

		if syncTarget.Status.SyncedResources[0].Resource != "services" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false, fmt.Sprintf("expected services resource, got %v\n\n%s", syncTarget.Status.SyncedResources, toYAML(t, syncTarget))
		}

		return true, ""
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
	virtualWorkspaceConfig = rest.AddUserAgent(rest.CopyConfig(virtualWorkspaceConfig), t.Name())

	virtualWorkspaceiscoverClusterClient, err := kcpdiscovery.NewForConfig(virtualWorkspaceConfig)
	require.NoError(t, err)

	t.Logf("Wait for service API from synctarget workspace to be served in synctarget virtual workspace.")
	require.Eventually(t, func() bool {
		_, existingAPIResourceLists, err := virtualWorkspaceiscoverClusterClient.ServerGroupsAndResources()
		if err != nil {
			return false
		}
		// requiredAPIResourceList includes all core APIs plus services API
		return len(cmp.Diff([]*metav1.APIResourceList{
			requiredAPIResourceListWithService(computeClusterName, computeClusterName)}, sortAPIResourceList(existingAPIResourceLists))) == 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should be authorized to access downstream clusters")
	framework.Eventually(t, func() (bool, string) {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		done := conditions.IsTrue(syncTarget, workloadv1alpha1.SyncerAuthorized)
		var reason string
		if !done {
			condition := conditions.Get(syncTarget, workloadv1alpha1.SyncerAuthorized)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for SyncTarget to be authorized: %s: %s", condition.Reason, condition.Message)
			} else {
				reason = "Not done waiting for SyncTarget to be authorized: no condition present"
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind to location workspace")
	framework.NewBindCompute(t, computeClusterName.Path(), source,
		framework.WithAPIExportsWorkloadBindOption("kubernetes"),
	).Bind(t)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(computeClusterName.Path()).CoreV1().Services("default").List(ctx, metav1.ListOptions{})
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
	_, err = kubeClusterClient.Cluster(computeClusterName.Path()).CoreV1().Services("default").Create(ctx, &corev1.Service{
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

func toYAML(t *testing.T, obj interface{}) string {
	t.Helper()
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
