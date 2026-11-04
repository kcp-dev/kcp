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

package replication

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestClusterCachedResourceNamedClusters serves a ClusterCachedResourceEndpointSlice
// named "clusters", whose VW URL carries a "clusters" segment before the
// clusters/<cluster> one.
func TestClusterCachedResourceNamedClusters(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.ParallelSuite)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kcpDynClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp dynamic cluster client for server")

	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	providerClusterName := logicalcluster.Name(providerWS.Spec.Cluster)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	const name = "clusters"
	gvr := wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs")

	t.Logf("Creating Sheriff CRD and APIResourceSchema in %q", providerPath)
	crd := wildwest.CRD(t, metav1.GroupResource(gvr.GroupResource()))
	sch, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "today")
	require.NoError(t, err)
	wildwest.Create(t, providerPath, kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource(gvr.GroupResource()))
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(providerPath).Discovery(), gvr.GroupVersion())
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), sch, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating a Sheriff in %q", providerPath)
	sheriff, err := createSheriff(t.Context(), kcpDynClusterClient, providerClusterName, &wildwestv1alpha1.Sheriff{
		ObjectMeta: metav1.ObjectMeta{Name: "sheriff-1"},
	})
	require.NoError(t, err)

	t.Logf("Creating ClusterCachedResource %q in %q", name, providerPath)
	_, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResources().Create(t.Context(), &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource(gvr),
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		ccr, err := kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResources().Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if ccr.Status.ResourceCounts == nil || ccr.Status.ResourceCounts.Cache != 1 {
			return false, fmt.Sprintf("waiting for 1 cached object, have %v", ccr.Status.ResourceCounts)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting until ClusterCachedResource replicates sheriff-1")

	t.Logf("Creating APIExport %q in %q", name, providerPath)
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "sheriffs",
					Schema: "today.sheriffs.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
							Reference: corev1.TypedLocalObjectReference{
								APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
								Kind:     "ClusterCachedResourceEndpointSlice",
								Name:     name,
							},
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating ClusterCachedResourceEndpointSlice %q in %q", name, providerPath)
	_, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Create(t.Context(), &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{Name: name},
			APIExport:             cachev1alpha1.ExportBindingReference{Name: name},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	var endpointSlice *cachev1alpha1.ClusterCachedResourceEndpointSlice
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		endpointSlice, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(t.Context(), name, metav1.GetOptions{})
		return endpointSlice, err
	}, kcptestinghelpers.Is(cachev1alpha1.APIExportValid), "ClusterCachedResourceEndpointSlice APIExport reference should become ready")

	t.Logf("Binding %s|%s in %s", providerPath, name, consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: providerPath.String(),
						Name: name,
					},
				},
			},
		}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("failed to create APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Second, "waiting to create apibinding")

	t.Logf("Waiting for VW URL of the endpoint slice")
	vwCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		endpointSlice, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		var found bool
		vwCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), cfg, consumerWorkspace, framework.ReplicationVirtualWorkspaceURLs(endpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", endpointSlice.Status.ClusterCachedResourceEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.True(t, strings.HasSuffix(vwCfg.Host, "/"+name), "VW URL %q must end in the endpoint slice name", vwCfg.Host)
	vwClient, err := kcpdynamic.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Logf("Getting the sheriff through the VW by consumer cluster")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := getSheriff(t.Context(), vwClient, consumerClusterName, sheriff.Name)
		return err == nil, fmt.Sprintf("error getting sheriff through VW: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
