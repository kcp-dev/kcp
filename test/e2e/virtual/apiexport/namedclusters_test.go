/*
Copyright 2026 The kcp Authors.

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

package apiexport

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIExportNamedClusters serves an APIExport named "clusters", whose VW
// URL carries a "clusters" segment before the clusters/<cluster> one.
func TestAPIExportNamedClusters(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.ParallelSuite)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	cowboysGVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}
	consumerCowboys := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default")

	t.Log("Install cowboys APIResourceSchema into provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Log("Create APIExport named clusters in provider")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "clusters",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind provider APIExport in consumer")
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExport.Name,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a cowboy in the consumer workspace")
	cowboy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "wildwest.dev/v1alpha1",
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name": "woody",
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := consumerCowboys.Create(t.Context(), cowboy, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating cowboy: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for VW URL of the provider APIExport")
	providerVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		providerVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), cfg, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	require.True(t, strings.HasSuffix(providerVWCfg.Host, "/clusters"), "VW URL %q must end in the export name", providerVWCfg.Host)
	providerVWClient, err := kcpdynamic.NewForConfig(providerVWCfg)
	require.NoError(t, err)

	t.Log("Get the cowboy through the provider VW by consumer cluster")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := providerVWClient.Cluster(consumerClusterName.Path()).Resource(cowboysGVR).Namespace("default").Get(t.Context(), cowboy.GetName(), metav1.GetOptions{})
		return err == nil, fmt.Sprintf("error getting cowboy through VW: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("List cowboys through the provider VW across clusters")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := providerVWClient.Resource(cowboysGVR).List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing cowboys through VW: %v", err)
		}
		return len(list.Items) == 1, fmt.Sprintf("expected 1 cowboy, got %d", len(list.Items))
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
