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

package apiexport

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/util/sets"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

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
		require.NoError(t, err)
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
