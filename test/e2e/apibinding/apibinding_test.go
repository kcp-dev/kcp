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
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
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
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
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

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	providerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("service-provider-1"))
	consumerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-1-bound-against-1"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	t.Logf("Create an APIExport today-cowboys in %q", providerPath)
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIExport other-export in %q", providerPath)
	otherAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-export",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, otherAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerPath, providerPath)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
		},
	}

	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure we can get the APIBinding we just created")
	apiBinding, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(ctx, apiBinding.Name, metav1.GetOptions{})
	require.NoError(t, err)

	patchedBinding := apiBinding.DeepCopy()
	patchedBinding.Spec.Reference.Export.Name = "other-export"
	mergePatch, err := jsonpatch.CreateMergePatch(encodeJSON(t, apiBinding), encodeJSON(t, patchedBinding))
	require.NoError(t, err)

	t.Logf("Try to patch the APIBinding to point at other-export and make sure we get the expected error")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Patch(ctx, apiBinding.Name, types.MergePatchType, mergePatch, metav1.PatchOptions{})
		expected := "APIExport reference must not be changed"
		if strings.Contains(err.Error(), expected) {
			return true, ""
		}
		return false, fmt.Sprintf("Expecting error to contain %q but got %v", expected, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func TestAPIBinding(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	provider1Path, provider1 := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("service-provider-1"))
	provider1ClusterName := logicalcluster.Name(provider1.Spec.Cluster)
	provider2Path, provider2 := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("service-provider-2"))
	provider2ClusterName := logicalcluster.Name(provider2.Spec.Cluster)
	consumer1Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-1-bound-against-1"))
	consumer2Path, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-2-bound-against-1"))
	consumer3Path, consumer3Workspace := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-3-bound-against-2"))
	consumer3ClusterName := logicalcluster.Name(consumer3Workspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	shardVirtualWorkspaceURLs := sets.New[string]()
	t.Logf("Getting a list of VirtualWorkspaceURLs assigned to Shards")
	shards, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	// Filtering out shards that are not schedulable
	var shardItems []corev1alpha1.Shard
	for _, s := range shards.Items {
		if _, ok := s.Annotations["experimental.core.kcp.io/unschedulable"]; !ok {
			shardItems = append(shardItems, s)
		}
	}
	require.Eventually(t, func() bool {
		for _, s := range shardItems {
			if _, ok := s.Annotations["experimental.core.kcp.io/unschedulable"]; !ok {
				if len(s.Spec.VirtualWorkspaceURL) == 0 {
					t.Logf("%q shard hasn't had assigned a virtual workspace URL", s.Name)
					return false
				}
				shardVirtualWorkspaceURLs.Insert(s.Spec.VirtualWorkspaceURL)
			}
		}
		return true
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected all Shards to have a VirtualWorkspaceURL assigned")

	exportName := "today-cowboys"
	serviceProviderWorkspaces := []logicalcluster.Path{provider1Path, provider2Path}
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

	bindConsumerToProvider := func(consumerWorkspace logicalcluster.Path, providerPath logicalcluster.Path) {
		t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerWorkspace, providerPath)
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: providerPath.String(),
						Name: "today-cowboys",
					},
				},
			},
		}

		framework.Eventually(t, func() (bool, string) {
			_, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		t.Logf("Make sure %s API group does NOT show up in workspace %q group discovery", wildwest.GroupName, providerPath)
		providerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err)

		groups, err := providerWorkspaceClient.Cluster(providerPath).Discovery().ServerGroups()
		require.NoError(t, err, "error retrieving %q group discovery", providerPath)
		require.False(t, groupExists(groups, wildwest.GroupName),
			"should not have seen %s API group in %q group discovery", wildwest.GroupName, providerPath)

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
						Path: provider2Path.String(),
						Name: "today-cowboys",
					},
				},
			},
		}

		framework.Eventually(t, func() (bool, string) {
			_, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		t.Logf("Make sure %s cowboys2 conflict with already bound %s cowboys", provider2Path, providerPath)
		framework.EventuallyCondition(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, "cowboys2", metav1.GetOptions{})
		}, framework.IsNot(apisv1alpha1.InitialBindingCompleted).WithReason(apisv1alpha1.NamingConflictsReason), "expected naming conflict")
	}

	verifyVirtualWorkspaceURLs := func(serviceProviderClusterName logicalcluster.Name) {
		var expectedURLs []string
		for _, urlString := range sets.List[string](shardVirtualWorkspaceURLs) {
			u, err := url.Parse(urlString)
			require.NoError(t, err, "error parsing %q", urlString)
			u.Path = path.Join(u.Path, "services", "apiexport", serviceProviderClusterName.String(), exportName)
			expectedURLs = append(expectedURLs, u.String())
		}

		t.Logf("Make sure the APIExport gets status.virtualWorkspaceURLs set")
		framework.Eventually(t, func() (bool, string) {
			e, err := kcpClusterClient.Cluster(serviceProviderClusterName.Path()).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
			if err != nil {
				t.Logf("Unexpected error getting APIExport %s|%s: %v", serviceProviderClusterName.Path(), exportName, err)
			}

			var actualURLs []string
			//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
			for _, u := range e.Status.VirtualWorkspaces {
				actualURLs = append(actualURLs, u.URL)
			}

			if !reflect.DeepEqual(expectedURLs, actualURLs) {
				return false, fmt.Sprintf("Unexpected URLs. Diff: %s", cmp.Diff(expectedURLs, actualURLs))
			}

			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIExport %s|%s didn't get status.virtualWorkspaceURLs set correctly",
			serviceProviderClusterName, exportName)
	}

	consumersOfServiceProvider1 := []logicalcluster.Path{consumer1Path, consumer2Path}
	for _, consumerWorkspace := range consumersOfServiceProvider1 {
		bindConsumerToProvider(consumerWorkspace, provider1Path)
	}
	verifyVirtualWorkspaceURLs(provider1ClusterName)

	t.Logf("=== Binding %q to %q", consumer3Path, provider2Path)
	bindConsumerToProvider(consumer3Path, provider2Path)
	verifyVirtualWorkspaceURLs(provider2ClusterName)

	t.Logf("=== Testing identity wildcards")

	verifyWildcardList := func(consumerWorkspace logicalcluster.Path, expectedItems int) {
		t.Logf("Get APIBinding for workspace %s", consumerWorkspace.String())
		apiBinding, err := kcpClusterClient.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err, "error getting apibinding")

		identity := apiBinding.Status.BoundResources[0].Schema.IdentityHash
		gvrWithIdentity := wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys:" + identity)

		var names []string
		for _, shard := range shardItems {
			t.Logf("Doing a wildcard identity list for %v against %s workspace on shard %s", gvrWithIdentity, consumerWorkspace, shard.Name)
			shardDynamicClusterClients, err := kcpdynamic.NewForConfig(server.ShardSystemMasterBaseConfig(t, shard.Name))
			require.NoError(t, err)
			list, err := shardDynamicClusterClients.Resource(gvrWithIdentity).List(ctx, metav1.ListOptions{})
			if errors.IsNotFound(err) {
				continue // this shard doesn't have the resource because there is no binding
			}
			require.NoError(t, err, "error listing wildcard with identity")
			for _, cowboy := range list.Items {
				names = append(names, cowboy.GetName())
			}
		}

		cowboyName := fmt.Sprintf("cowboy-%s", consumerWorkspace.Base())
		require.Contains(t, names, cowboyName, "missing cowboy %q", cowboyName)
		require.Len(t, names, expectedItems, "unexpected # of cowboys")
	}

	for _, consumerWorkspace := range consumersOfServiceProvider1 {
		t.Logf("Verify %q bound to service provider 1 (%q) wildcard list works", consumerWorkspace, provider1Path)
		verifyWildcardList(consumerWorkspace, 2)
	}

	t.Logf("=== Verify that in %q (bound to %q) wildcard list works", consumer3Path, provider2Path)
	verifyWildcardList(consumer3Path, 1)

	t.Logf("=== Verify that %s|%s export virtual workspace shows cowboys", provider2Path, exportName)
	rawConfig, err := server.RawConfig()
	require.NoError(t, err)
	export2, err := kcpClusterClient.Cluster(provider2Path).ApisV1alpha1().APIExports().Get(ctx, exportName, metav1.GetOptions{})
	require.NoError(t, err)

	foundOnShards := 0
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	for _, vw := range export2.Status.VirtualWorkspaces {
		vw2ClusterClient, err := kcpdynamic.NewForConfig(apiexportVWConfig(t, rawConfig, vw.URL))
		require.NoError(t, err)

		t.Logf("Listing %s|%s cowboys via virtual workspace %s/clusters/%s", provider2Path, exportName, vw.URL, consumer3ClusterName)
		gvr := wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys")
		list, err := vw2ClusterClient.Cluster(consumer3ClusterName.Path()).Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("Error: %v", err)
			continue
		}
		require.Equal(t, 1, len(list.Items), "unexpected # of cowboys through virtual workspace with explicit workspace")
		foundOnShards++

		t.Logf("Listing %s|%s cowboys via virtual workspace wildcard list", provider2Path, exportName)
		list, err = vw2ClusterClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing through virtual workspace wildcard")
		require.Equal(t, 1, len(list.Items), "unexpected # of cowboys through virtual workspace with wildcard")
	}
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	require.Equal(t, 1, foundOnShards, "cowboys not found exactly on one shard, but on %d/%d", foundOnShards, len(export2.Status.VirtualWorkspaces))
}

func apiexportVWConfig(t *testing.T, kubeconfig clientcmdapi.Config, url string) *rest.Config {
	t.Helper()

	virtualWorkspaceRawConfig := kubeconfig.DeepCopy()
	virtualWorkspaceRawConfig.Clusters["apiexport"] = kubeconfig.Clusters["base"].DeepCopy()
	virtualWorkspaceRawConfig.Clusters["apiexport"].Server = url
	virtualWorkspaceRawConfig.Contexts["apiexport"] = kubeconfig.Contexts["base"].DeepCopy()
	virtualWorkspaceRawConfig.Contexts["apiexport"].Cluster = "apiexport"

	config, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "apiexport", nil, nil).ClientConfig()
	require.NoError(t, err)

	return rest.AddUserAgent(rest.CopyConfig(config), t.Name())
}

func encodeJSON(t *testing.T, obj interface{}) []byte {
	t.Helper()
	ret, err := json.Marshal(obj)
	require.NoError(t, err)
	return ret
}
