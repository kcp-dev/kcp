/*
Copyright 2025 The KCP Authors.

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

package dynamicrestmapper

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/integration/dynamicrestmapper/assets"
	"github.com/kcp-dev/kcp/test/integration/framework"
)

func TestDynamicRestMapper(t *testing.T) {
	t.Parallel()

	server, kcpClientSet, _ := framework.StartTestServer(t)

	name := "test-workspace-" + strings.ToLower(base36.Encode(rand.Uint64())[:8])

	cfg, err := server.ClientConfig.ClientConfig()
	require.NoError(t, err, "error creating client config")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	loopbackExtensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(server.Server.GenericConfig.LoopbackClientConfig)
	require.NoError(t, err, "error creating loopback apiextensions cluster client")

	workspace := kcptesting.NewLowLevelWorkspaceFixture(t, kcpClientSet, kcpClientSet, core.RootCluster.Path(), kcptesting.WithName("%s", name))

	logicalClusterName := logicalcluster.Name(workspace.Spec.Cluster)

	drm := server.Server.ExtraConfig.DynamicRESTMapper.ForCluster(logicalClusterName)

	t.Logf("Testing if we can resolve the ConfigMap kind")
	var gvk schema.GroupVersionKind
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "configmaps",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")
	require.NoError(t, err)

	require.Equal(t, "ConfigMap", gvk.Kind)

	t.Logf("Install kubecluster CRD into workspace %q", workspace.Name)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClientSet.Cluster(logicalClusterName.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(logicalClusterName.Path()), mapper, nil, "crd_kubecluster.yaml", assets.EmbeddedResources)
	require.NoError(t, err)

	t.Logf("Waiting for CRD to be established")
	crdName := "kubeclusters.contrib.kcp.io"
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := extensionsClusterClient.Cluster(logicalClusterName.Path()).CustomResourceDefinitions().Get(t.Context(), crdName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	t.Logf("Testing if we can resolve the KubeCluster v1alpha1 kind")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "contrib.kcp.io",
			Version:  "v1alpha1",
			Resource: "kubeclusters",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	require.NoError(t, err)
	require.Equal(t, "KubeCluster", gvk.Kind)

	t.Logf("Updating the KubeCluster kind with v1alpha2, not serving v1alpha1 anymore")
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(logicalClusterName.Path()), mapper, nil, "crd_kubecluster_updates.yaml", assets.EmbeddedResources)
	require.NoError(t, err)

	t.Logf("Waiting for CRD to be established")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := extensionsClusterClient.Cluster(logicalClusterName.Path()).CustomResourceDefinitions().Get(t.Context(), crdName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	t.Logf("Testing if we can resolve the KubeCluster v1alpha2 kind")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "contrib.kcp.io",
			Version:  "v1alpha2",
			Resource: "kubeclusters",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	require.NoError(t, err)
	require.Equal(t, "KubeCluster", gvk.Kind)

	t.Logf("Testing that the KubeCluster v1alpha1 kind is no longer discoverable")
	_, err = drm.KindsFor(schema.GroupVersionResource{
		Group:    "contrib.kcp.io",
		Version:  "v1alpha1",
		Resource: "kubeclusters",
	})
	require.Error(t, err, "KubeCluster v1alpha1 should not be discoverable anymore")

	t.Logf("Testing that CachedResourceEndpointSlice.cache.kcp.io can be resolved without explicit version")
	_, err = drm.RESTMapping(schema.GroupKind{
		Group: "cache.kcp.io",
		Kind:  "CachedResourceEndpointSlice",
	})
	require.NoError(t, err, "CachedResourceEndpointSlice.cache.kcp.io should be resolvable")

	t.Logf("Testing that creating Sheriff system CRD is reflected in the mapper")
	systemCRDsPath := logicalcluster.NewPath("system:system-crds")

	sheriffsCRD := apifixtures.NewSheriffsCRDWithVersions("wildsystem.dev", "v1alpha1")
	_, err = loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Create(t.Context(), sheriffsCRD, metav1.CreateOptions{})
	require.NoError(t, err, "creating a CRD in system:system-crds should succeed")

	t.Logf("Waiting for sheriffs system CRD to be established")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Get(t.Context(), sheriffsCRD.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for system CRD to be established")

	t.Logf("Testing if we can resolve the system Sheriff v1alpha1 kind")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "wildsystem.dev",
			Version:  "v1alpha1",
			Resource: "sheriffs",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for system CRD to be established")
	require.NoError(t, err)
	require.Equal(t, "Sheriff", gvk.Kind)

	t.Logf("Updating sheriffs system CRD with v1alpha2")

	sheriffsCRD = apifixtures.NewSheriffsCRDWithVersions("wildsystem.dev", "v1alpha1", "v1alpha2")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Get(t.Context(), sheriffsCRD.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		crd.Spec.Versions = sheriffsCRD.Spec.Versions
		_, err = loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Update(t.Context(), crd, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for system CRD to be updated")

	t.Logf("Waiting for updated system sheriffs CRD to be established")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Get(t.Context(), sheriffsCRD.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for system CRD to be established")

	t.Logf("Testing if we can resolve the system Sheriff v1alpha2 kind")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "wildsystem.dev",
			Version:  "v1alpha2",
			Resource: "sheriffs",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for system CRD to be established")
	require.NoError(t, err)
	require.Equal(t, "Sheriff", gvk.Kind)

	t.Logf("Deleting the sheriffs system CRD")

	err = loopbackExtensionsClusterClient.Cluster(systemCRDsPath).CustomResourceDefinitions().Delete(t.Context(), sheriffsCRD.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "deleting sheriffs system CRD should succeed")

	t.Logf("Testing that sheriffs system CRD is no longer discoverable")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "wildsystem.dev",
			Version:  "v1alpha2",
			Resource: "sheriffs",
		})
		return err != nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for sheriffs system resource to not be resolved anymore")
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
