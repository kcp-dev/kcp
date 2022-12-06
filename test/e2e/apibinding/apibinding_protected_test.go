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
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/apis/workload"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestProtectedAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	providerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	providerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(providerWorkspaceClient.Cluster(providerWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerWorkspace), mapper, nil, "apiresourceschema_tlsroutes.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gateway-api",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"latest.tlsroutes.gateway.networking.k8s.io"},
		},
	}
	_, err = kcpClusterClient.Cluster(providerWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the gateway-api export from %q", consumerWorkspace, providerWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gateway-api",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Cluster: providerWorkspace.String(),
					Name:    "gateway-api",
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Make sure APIBinding %q in workspace %q is completed and up-to-date", apiBinding.Name, consumerWorkspace)
	require.Eventually(t, func() bool {
		b, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		require.NoError(t, err)

		return conditions.IsTrue(b, apisv1alpha1.InitialBindingCompleted) &&
			conditions.IsTrue(b, apisv1alpha1.BindingUpToDate)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "APIBinding %q in workspace %q did not complete", apiBinding.Name, consumerWorkspace)

	t.Logf("Make sure gateway API resource shows up in workspace %q group version discovery", consumerWorkspace)
	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resources, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1alpha2")
		require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
		return resourceExists(resources, "tlsroutes")
	}, wait.ForeverTestTimeout, time.Millisecond*100, "consumer workspace %q discovery is missing tlsroutes resource", consumerWorkspace)
}

// TestProtectedAPIFromServiceExports is testing if we can access service exports
// from root workspace. See https://github.com/kcp-dev/kcp/issues/2184 for more details.
func TestProtectedAPIFromServiceExports(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rootWorkspace := logicalcluster.New("root")
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	t.Logf("Get tenancy APIExport from root workspace")
	workloadsRoot, err := kcpClusterClient.ApisV1alpha1().APIExports().Cluster(rootWorkspace).Get(ctx, workload.GroupName, metav1.GetOptions{})
	require.NoError(t, err)

	t.Logf("Construct VirtualWorkspace client")
	vwURL := workloadsRoot.Status.VirtualWorkspaces[0].URL
	cfgVW := server.RootShardSystemMasterBaseConfig(t)
	cfgVW.Host = vwURL

	vwClient, err := kcpclientset.NewForConfig(cfgVW)
	require.NoError(t, err)

	t.Logf("Make sure we can access tenancy API from VirtualWorkspace")
	_, err = vwClient.WorkloadV1alpha1().SyncTargets().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
}
