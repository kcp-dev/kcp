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
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"reflect"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingAPIExportReferenceImmutability(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("service-provider-1"))
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer-1-bound-against-1"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	t.Logf("Create an APIExport today-cowboys in %q", serviceProviderWorkspace)
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClusterClient.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerWorkspace, serviceProviderWorkspace)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Cluster: serviceProviderWorkspace.String(),
					Name:    "today-cowboys",
				},
			},
		},
	}

	_, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	apiBinding, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	require.NoError(t, err)

	patchedBinding := apiBinding.DeepCopy()
	patchedBinding.Spec.Reference.Export.Name = "other-export"
	mergePatch, err := jsonpatch.CreateMergePatch(encodeJSON(t, apiBinding), encodeJSON(t, patchedBinding))
	require.NoError(t, err)
	_, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, mergePatch, metav1.PatchOptions{})
	require.ErrorContains(t, err, "APIExport reference must not be changed")
}

func TestAPIBinding(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProvider1Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("service-provider-1"))
	serviceProvider2Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("service-provider-2"))
	consumer1Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer-1-bound-against-1"))
	consumer2Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer-2-bound-against-1"))
	consumer3Workspace := framework.NewWorkspaceFixture(t, server, orgClusterName, framework.WithName("consumer-3-bound-against-2"))

	cfg := server.BaseConfig(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	clusterWorkspaceShardVirtualWorkspaceURLs := sets.NewString()
	t.Logf("Getting a list of VirtualWorkspaceURLs assigned to ClusterWorkspaceShards")
	require.Eventually(t, func() bool {
		clusterWorkspaceShards, err := kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("unexpected error while listing clusterworkspaceshards, err %v", err)
			return false
		}
		for _, s := range clusterWorkspaceShards.Items {
			if len(s.Spec.VirtualWorkspaceURL) == 0 {
				t.Logf("%q shard hasn't had assigned a virtual workspace URL", s.Name)
				return false
			}
			clusterWorkspaceShardVirtualWorkspaceURLs.Insert(s.Spec.VirtualWorkspaceURL)
		}
		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected all ClusterWorkspaceShards to have a VirtualWorkspaceURL assigned")

	exportName := "today-cowboys"
	serviceProviderWorkspaces := []logicalcluster.Name{serviceProvider1Workspace, serviceProvider2Workspace}
	for _, serviceProviderWorkspace := range serviceProviderWorkspaces {
		t.Logf("Install today cowboys APIResourceSchema into %q", serviceProviderWorkspace)

		serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
		require.NoError(t, err)

		cowboysAPIExport := &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: exportName,
			},
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			},
		}
		t.Logf("Create an APIExport today-cowboys in %q", serviceProviderWorkspace)
		_, err = kcpClusterClient.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	bindConsumerToProvider := func(consumerWorkspace, providerWorkspace logicalcluster.Name) {
		t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerWorkspace, providerWorkspace)
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Cluster: providerWorkspace.String(),
						Name:    "today-cowboys",
					},
				},
			},
		}

		_, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Logf("Make sure %s API group does NOT show up in workspace %q group discovery", wildwest.GroupName, providerWorkspace)
		providerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		groups, err := providerWorkspaceClient.Cluster(providerWorkspace).Discovery().ServerGroups()
		require.NoError(t, err, "error retrieving %q group discovery", providerWorkspace)
		require.False(t, groupExists(groups, wildwest.GroupName),
			"should not have seen %s API group in %q group discovery", wildwest.GroupName, providerWorkspace)

		consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
		err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
			groups, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
			if err != nil {
				return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
			}
			return groupExists(groups, wildwest.GroupName), nil
		})
		require.NoError(t, err)

		t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
		resources, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		require.NoError(t, err, "error retrieving %q API discovery", consumerWorkspace)
		require.True(t, resourceExists(resources, "cowboys"), "%q discovery is missing cowboys resource", consumerWorkspace)

		wildwestClusterClient, err := wildwestclientset.NewForConfig(rest.CopyConfig(cfg))
		require.NoError(t, err)

		t.Logf("Make sure we can perform CRUD operations against consumer workspace %q for the bound API", consumerWorkspace)

		t.Logf("Make sure list shows nothing to start")
		cowboyClient := wildwestClusterClient.Cluster(consumerWorkspace).WildwestV1alpha1().Cowboys("default")
		cowboys, err := cowboyClient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", consumerWorkspace)
		require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside %q", consumerWorkspace)

		t.Logf("Create a cowboy CR in consumer workspace %q", consumerWorkspace)
		cowboyName := fmt.Sprintf("cowboy-%s", consumerWorkspace.Base())
		cowboy := &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cowboyName,
				Namespace: "default",
			},
		}
		_, err = cowboyClient.Create(ctx, cowboy, metav1.CreateOptions{})
		require.NoError(t, err, "error creating cowboy in %q", consumerWorkspace)

		t.Logf("Make sure there is 1 cowboy in consumer workspace %q", consumerWorkspace)
		cowboys, err = cowboyClient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys in %q", consumerWorkspace)
		require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboy in %q", consumerWorkspace)
		require.Equal(t, cowboyName, cowboys.Items[0].Name, "unexpected name for cowboy in %q", consumerWorkspace)

		t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from serviceProvider2 (which should conflict)", consumerWorkspace)
		apiBinding = &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys2",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Cluster: serviceProvider2Workspace.String(),
						Name:    "today-cowboys",
					},
				},
			},
		}

		_, err = kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Logf("Make sure %s cowboys2 conflict with already bound %s cowboys", serviceProvider2Workspace, providerWorkspace)
		require.Eventually(t, func() bool {
			b, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, "cowboys2", metav1.GetOptions{})
			require.NoError(t, err)
			return conditions.IsFalse(b, apisv1alpha1.InitialBindingCompleted) && conditions.GetReason(b, apisv1alpha1.InitialBindingCompleted) == apisv1alpha1.NamingConflictsReason
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected naming conflict")
	}

	verifyVirtualWorkspaceURLs := func(serviceProviderWorkspace logicalcluster.Name) {
		var expectedURLs []string
		for _, urlString := range clusterWorkspaceShardVirtualWorkspaceURLs.List() {
			u, err := url.Parse(urlString)
			require.NoError(t, err, "error parsing %q", urlString)
			u.Path = path.Join(u.Path, "services", "apiexport", serviceProviderWorkspace.String(), exportName)
			expectedURLs = append(expectedURLs, u.String())
		}

		t.Logf("Make sure the APIExport gets status.virtualWorkspaceURLs set")
		framework.Eventually(t, func() (bool, string) {
			e, err := kcpClusterClient.Cluster(serviceProviderWorkspace).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
			if err != nil {
				t.Logf("Unexpected error getting APIExport %s|%s: %v", serviceProviderWorkspace, exportName, err)
			}

			var actualURLs []string
			for _, u := range e.Status.VirtualWorkspaces {
				actualURLs = append(actualURLs, u.URL)
			}

			if !reflect.DeepEqual(expectedURLs, actualURLs) {
				return false, fmt.Sprintf("Unexpected URLs. Diff: %s", cmp.Diff(expectedURLs, actualURLs))
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIExport %s|%s didn't get status.virtualWorkspaceURLs set correctly",
			serviceProviderWorkspace, exportName)
	}

	consumersOfServiceProvider1 := []logicalcluster.Name{consumer1Workspace, consumer2Workspace}
	for _, consumerWorkspace := range consumersOfServiceProvider1 {
		bindConsumerToProvider(consumerWorkspace, serviceProvider1Workspace)
	}
	verifyVirtualWorkspaceURLs(serviceProvider1Workspace)

	t.Logf("=== Binding %q to %q", consumer3Workspace, serviceProvider2Workspace)
	bindConsumerToProvider(consumer3Workspace, serviceProvider2Workspace)
	verifyVirtualWorkspaceURLs(serviceProvider2Workspace)

	t.Logf("=== Testing identity wildcards")

	verifyWildcardList := func(consumerWorkspace logicalcluster.Name, expectedItems int) {
		t.Logf("Get %s workspace shard and create a shard client that is able to do wildcard requests", consumerWorkspace)
		shardDynamicClusterClients, err := kcpdynamic.NewForConfig(rootShardCfg)
		require.NoError(t, err)

		t.Logf("Get APIBinding for workspace %s", consumerWorkspace.String())
		apiBinding, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err, "error getting apibinding")

		identity := apiBinding.Status.BoundResources[0].Schema.IdentityHash
		gvrWithIdentity := wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys:" + identity)

		t.Logf("Doing a wildcard identity list for %v against %s workspace shard", gvrWithIdentity, consumerWorkspace)
		wildcardIdentityClient := shardDynamicClusterClients.Resource(gvrWithIdentity)
		list, err := wildcardIdentityClient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing wildcard with identity")

		require.Len(t, list.Items, expectedItems, "unexpected # of cowboys")

		var names []string
		for _, cowboy := range list.Items {
			names = append(names, cowboy.GetName())
		}

		cowboyName := fmt.Sprintf("cowboy-%s", consumerWorkspace.Base())
		require.Contains(t, names, cowboyName, "missing cowboy %q", cowboyName)
	}

	for _, consumerWorkspace := range consumersOfServiceProvider1 {
		t.Logf("Verify %q bound to service provider 1 (%q) wildcard list works", consumerWorkspace, serviceProvider1Workspace)
		verifyWildcardList(consumerWorkspace, 2)
	}

	t.Logf("=== Verify that in %q (bound to %q) wildcard list works", consumer3Workspace, serviceProvider2Workspace)
	verifyWildcardList(consumer3Workspace, 1)

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	t.Logf("Smoke test %s|today-cowboys virtual workspace with explicit /cluster/%s", serviceProvider2Workspace, consumer3Workspace)
	vw2ClusterClient, err := kcpdynamic.NewForConfig(apiexportVWConfig(t, rawConfig, serviceProvider2Workspace, "today-cowboys"))
	require.NoError(t, err)
	gvr := wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys")
	list, err := vw2ClusterClient.Cluster(consumer3Workspace).Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing through virtual workspace with explicit workspace")
	require.Equal(t, 1, len(list.Items), "unexpected # of cowboys through virtual workspace with explicit workspace")

	t.Logf("Smoke test %s|today-cowboys virtual workspace with wildcard", serviceProvider2Workspace)
	list, err = vw2ClusterClient.Resource(gvr).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing through virtual workspace wildcard")
	require.Equal(t, 1, len(list.Items), "unexpected # of cowboys through virtual workspace with wildcard")
}

func apiexportVWConfig(t *testing.T, kubeconfig clientcmdapi.Config, clusterName logicalcluster.Name, apiexportName string) *rest.Config {
	virtualWorkspaceRawConfig := kubeconfig.DeepCopy()
	virtualWorkspaceRawConfig.Clusters["apiexport"] = kubeconfig.Clusters["base"].DeepCopy()
	virtualWorkspaceRawConfig.Clusters["apiexport"].Server = fmt.Sprintf("%s/services/apiexport/%s/%s/", kubeconfig.Clusters["base"].Server, clusterName.String(), apiexportName)
	virtualWorkspaceRawConfig.Contexts["apiexport"] = kubeconfig.Contexts["base"].DeepCopy()
	virtualWorkspaceRawConfig.Contexts["apiexport"].Cluster = "apiexport"

	config, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "apiexport", nil, nil).ClientConfig()
	require.NoError(t, err)

	return rest.AddUserAgent(rest.CopyConfig(config), t.Name())
}

func encodeJSON(t *testing.T, obj interface{}) []byte {
	ret, err := json.Marshal(obj)
	require.NoError(t, err)
	return ret
}
