/*
Copyright 2021 The KCP Authors.

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

package apibinding

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestProtectedAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	providerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(providerWorkspaceClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_tlsroutes.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gateway-api",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "tlsroutes",
					Group:  "gateway.networking.k8s.io",
					Schema: "latest.tlsroutes.gateway.networking.k8s.io",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the gateway-api export from %q", consumerPath, providerPath)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gateway-api",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: "gateway-api",
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create APIBinding")

	t.Logf("Make sure APIBinding %q in workspace %q is completed and up-to-date", apiBinding.Name, consumerPath)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha1.InitialBindingCompleted))
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha1.BindingUpToDate))

	t.Logf("Make sure gateway API resource shows up in workspace %q group version discovery", consumerPath)
	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resources, err := consumerWorkspaceClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1alpha2")
		require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
		return resourceExists(resources, "tlsroutes")
	}, wait.ForeverTestTimeout, time.Millisecond*100, "consumer workspace %q discovery is missing tlsroutes resource", consumerPath)
}

// TestProtectedAPIFromServiceExports is testing if we can access service exports
// from root workspace. See https://github.com/kcp-dev/kcp/issues/2184 for more details.
func TestProtectedAPIFromServiceExports(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rootWorkspace := logicalcluster.NewPath("root")
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	t.Logf("Get tenancy APIExport from root workspace")
	tenanciesRoot, err := kcpClusterClient.ApisV1alpha2().APIExports().Cluster(rootWorkspace).Get(ctx, tenancy.GroupName, metav1.GetOptions{})
	require.NoError(t, err)

	t.Logf("Construct VirtualWorkspace client")
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	vwURL := tenanciesRoot.Status.VirtualWorkspaces[0].URL
	cfgVW := server.RootShardSystemMasterBaseConfig(t)
	cfgVW.Host = vwURL

	vwClient, err := kcpclientset.NewForConfig(cfgVW)
	require.NoError(t, err)

	t.Logf("Make sure we can access tenancy API from VirtualWorkspace")
	_, err = vwClient.TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
}
