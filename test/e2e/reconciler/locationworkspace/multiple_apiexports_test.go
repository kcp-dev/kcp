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
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/client/dynamic"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMultipleExports(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)

	kcpClients, err := clientset.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceSchemaClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	t.Logf("Install service APIResourceSchema into service schema workspace %q", serviceSchemaClusterName)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(serviceSchemaClusterName).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(serviceSchemaClusterName), mapper, nil, "apiresourceschema_service.yaml", testFiles)
	require.NoError(t, err)
	t.Logf("Create an APIExport for it")
	serviceAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "services",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"test.services.core"},
		},
	}
	_, err = kcpClients.Cluster(serviceSchemaClusterName).ApisV1alpha1().APIExports().Create(ctx, serviceAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	ingressSchemaClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName)
	t.Logf("Install ingress APIResourceSchema into ingress schema workspace %q", ingressSchemaClusterName)
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(ingressSchemaClusterName).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(ingressSchemaClusterName), mapper, nil, "apiresourceschema_ingress.yaml", testFiles)
	require.NoError(t, err)
	t.Logf("Create an APIExport for it")
	ingressAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingresses",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"test.ingresses.networking.k8s.io"},
		},
	}
	_, err = kcpClients.Cluster(ingressSchemaClusterName).ApisV1alpha1().APIExports().Create(ctx, ingressAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	syncTargetName := fmt.Sprintf("synctarget-%d", +rand.Intn(1000000))
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncTarget := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithExtraResources("ingresses.networking.k8s.io", "services"),
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
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Logf("Patch synctarget with new export")
	patchData := fmt.Sprintf(
		`{"spec":{"supportedAPIExports":[{"workspace":{"path":%q,"exportName":"services"}},{"workspace":{"path":%q,"exportName":"ingresses"}}]}}`, serviceSchemaClusterName.String(), ingressSchemaClusterName.String())
	_, err = kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 2 {
			return false
		}

		if syncTarget.Status.SyncedResources[1].Resource != "services" ||
			syncTarget.Status.SyncedResources[1].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "ingresses" ||
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
	require.Eventually(t, func() bool {
		_, existingAPIResourceLists, err := virtualWorkspaceiscoverClusterClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()

		if err != nil {
			return false
		}
		requiredIngressAPIResourceList := &metav1.APIResourceList{
			TypeMeta: metav1.TypeMeta{
				Kind:       "APIResourceList",
				APIVersion: "v1",
			},
			GroupVersion: "networking.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{
					Kind:               "Ingress",
					Name:               "ingresses",
					SingularName:       "ingress",
					Namespaced:         true,
					Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
					StorageVersionHash: discovery.StorageVersionHash(ingressSchemaClusterName, "networking.k8s.io", "v1", "Ingress"),
				},
				{
					Kind:               "Ingress",
					Name:               "ingresses/status",
					SingularName:       "",
					Namespaced:         true,
					Verbs:              metav1.Verbs{"get", "patch", "update"},
					StorageVersionHash: "",
				},
			},
		}

		return len(cmp.Diff([]*metav1.APIResourceList{
			requiredIngressAPIResourceList, requiredAPIResourceListWithService(computeClusterName, serviceSchemaClusterName)}, sortAPIResourceList(existingAPIResourceLists))) == 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

}
