/*
Copyright 2025 The kcp Authors.

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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/kube-openapi/pkg/util/sets"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportOpenAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	serviceProviderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, serviceProviderPath, []string{"user-1"}, nil, false)

	setUpServiceProvider(t, dynamicClusterClient, kcpClients, true, serviceProviderPath, cfg, nil)

	t.Logf("set up binding")
	bindConsumerToProvider(t, consumerPath, serviceProviderPath, kcpClients, cfg)

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerWorkspace.Name)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(serviceProviderPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), "today-cowboys", metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Checking /openapi/v3 paths for %q", orgPath)
	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		openAPIV3 := wildwestVCClusterClient.Cluster(consumerClusterName.Path()).Discovery().OpenAPIV3()
		paths, err := openAPIV3.Paths()
		if err != nil {
			return false, err.Error()
		}
		got := sets.NewString()
		for path := range paths {
			got.Insert(path)
		}
		expected := sets.NewString(
			"api",
			"apis",
			"apis/apis.kcp.io",
			"apis/apis.kcp.io/v1alpha1",
			"apis/wildwest.dev",
			"apis/wildwest.dev/v1alpha1",
			"version",
		)
		if expected.Difference(got).Len() > 0 {
			return false, fmt.Sprintf("expected %d OpenAPI URLs, got %d", expected.Len(), got.Len())
		}
		return true, "found OpenAPI URLs"
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

// TestAPIExportOpenAPI_MultipleKinds verifies that when an APIExport binds
// multiple Kinds under the same group/version, the APIExport virtual workspace's
// /openapi/v3/apis/<group>/<version> document contains schemas for ALL of those
// Kinds — not just one. This used to fail because per-CRD OpenAPI was cached
// individually on each shard and a single request would only surface one Kind.
func TestAPIExportOpenAPI_MultipleKinds(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	serviceProviderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, serviceProviderPath, []string{"user-1"}, nil, false)

	// Install BOTH cowboys and sheriffs APIResourceSchemas under wildwest.dev/v1alpha1.
	t.Logf("Install cowboys and sheriffs APIResourceSchemas into %q", serviceProviderPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(
		kcpClients.Cluster(serviceProviderPath).Discovery(),
	))
	require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(serviceProviderPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))
	require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(serviceProviderPath), mapper, nil, "apiresourceschema_sheriffs.yaml", testFiles))

	t.Logf("Create an APIExport referencing both Kinds in the same group/version")
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest-multi"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{Name: "cowboys", Group: "wildwest.dev", Schema: "today.cowboys.wildwest.dev", Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}},
				{Name: "sheriffs", Group: "wildwest.dev", Schema: "today.sheriffs.wildwest.dev", Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}},
			},
		},
	}
	_, err = kcpClients.Cluster(serviceProviderPath).ApisV1alpha2().APIExports().Create(t.Context(), export, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind consumer %q to %q", consumerPath, serviceProviderPath)
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest-multi"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: serviceProviderPath.String(), Name: "wildwest-multi"},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), binding, metav1.CreateOptions{})
		return err == nil || apierrors.IsAlreadyExists(err), fmt.Sprintf("creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Resolve APIExport VW URL for consumer %q", consumerWorkspace.Name)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClients.Cluster(serviceProviderPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), "wildwest-multi", metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting for APIExportEndpointSlice: %v", err)
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(slice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for VW URL: %v", slice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	// The bug: a single fetch of /openapi/v3/apis/wildwest.dev/v1alpha1 returns
	// only ONE of the two Kinds (whichever the answering shard pod has cached).
	// A correct merged document must contain BOTH Cowboy and Sheriff.
	t.Logf("Fetch /openapi/v3 for wildwest.dev/v1alpha1 — must include all bound Kinds in a single response")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		openAPIV3 := wildwestVCClusterClient.Cluster(consumerClusterName.Path()).Discovery().OpenAPIV3()
		paths, err := openAPIV3.Paths()
		if err != nil {
			return false, fmt.Sprintf("fetching OpenAPI v3 paths: %v", err)
		}
		gv, ok := paths["apis/wildwest.dev/v1alpha1"]
		if !ok {
			return false, fmt.Sprintf("path apis/wildwest.dev/v1alpha1 not in OpenAPI paths: %v", paths)
		}
		raw, err := gv.Schema("application/json")
		if err != nil {
			return false, fmt.Sprintf("fetching schema: %v", err)
		}
		var doc struct {
			Components struct {
				Schemas map[string]struct {
					GVKs []struct {
						Group   string `json:"group"`
						Version string `json:"version"`
						Kind    string `json:"kind"`
					} `json:"x-kubernetes-group-version-kind"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Sprintf("unmarshalling schema: %v", err)
		}
		got := sets.NewString()
		for _, s := range doc.Components.Schemas {
			for _, gvk := range s.GVKs {
				if gvk.Group == "wildwest.dev" && gvk.Version == "v1alpha1" {
					got.Insert(gvk.Kind)
				}
			}
		}
		expected := sets.NewString("Cowboy", "Sheriff")
		missing := expected.Difference(got)
		if missing.Len() > 0 {
			return false, fmt.Sprintf("OpenAPI for wildwest.dev/v1alpha1 missing Kinds %v (got %v)", missing.List(), got.List())
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*200)
}
