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

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestProtectedAPI(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	providerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", providerWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(providerWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(providerWorkspace), mapper, "apiresourceschema_tlsroutes.yaml", testFiles)
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
	_, err = kcpClients.Cluster(providerWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in consumer workspace %q that points to the gateway-api export from %q", consumerWorkspace, providerWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gateway-api",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       providerWorkspace.String(),
					ExportName: "gateway-api",
				},
			},
		},
	}

	_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Make sure APIBinding %q in workspace %q is completed and up-to-date", apiBinding.Name, consumerWorkspace)
	require.Eventually(t, func() bool {
		b, err := kcpClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
		require.NoError(t, err)

		return conditions.IsTrue(b, apisv1alpha1.InitialBindingCompleted) &&
			conditions.IsTrue(b, apisv1alpha1.BindingUpToDate)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "APIBinding %q in workspace %q did not complete", apiBinding.Name, consumerWorkspace)

	t.Logf("Make sure gateway API resource shows up in workspace %q group version discovery", consumerWorkspace)
	resources, err := kcpClients.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1alpha2")
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
	require.True(t, resourceExists(resources, "tlsroutes"), "consumer workspace %q discovery is missing tlsroutes resource", consumerWorkspace)
}
