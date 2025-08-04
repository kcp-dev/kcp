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

package replication

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery/cached/memory"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/kube-openapi/pkg/util/sets"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCachedResourceVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client for server")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	serviceProviderPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, serviceProviderPath, []string{"user-1"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, consumerPath, []string{"user-1"}, nil, false)

	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClients, true, serviceProviderPath, cfg, nil)
	bindConsumerToProvider(ctx, t, consumerPath, serviceProviderPath, kcpClients, cfg)
	cowboyName1 := createCowboyInConsumer(ctx, t, consumerPath, wildwestClusterClient, nil)
	createCachedResourceAndCachedResourceEndpointSliceInConsumer(ctx, t, consumerPath, kcpClients, "cow")

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerWorkspace.Name)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(serviceProviderPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClients, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Verifying that the virtual workspace includes the cowboy resource")
	wildwestVCClusterClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	cowboysProjected, err := wildwestVCClusterClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, len(cowboysProjected.Items))

	t.Logf("Verify that the virtual workspace includes apibindings")
	discoveryVCClusterClient, err := kcpdiscovery.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)
	resources, err := discoveryVCClusterClient.ServerResourcesForGroupVersion(apisv1alpha2.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving APIExport discovery")
	require.True(t, resourceExists(resources, "apibindings"), "missing apibindings")

	resources, err = discoveryVCClusterClient.ServerResourcesForGroupVersion(apisv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving APIExport discovery")
	require.True(t, resourceExists(resources, "apibindings"), "missing apibindings")

	t.Logf("Waiting for CachedResource to have a virtual workspace URL for the consumer workspace %q", consumerWorkspace.Name)
	cachedResourceVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cachedResourceEndpointSlice, err := kcpClients.Cluster(consumerPath).CacheV1alpha1().CachedResourceEndpointSlices().
			Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		cachedResourceVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClients, consumerWorkspace,
			framework.ReplicationVirtualWorkspaceURLs(cachedResourceEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", cachedResourceEndpointSlice.Status.CachedResourceEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	user1CachedResourceVWCfg := framework.StaticTokenUserConfig("user-1", cachedResourceVWCfg)
	wwUser1CachedResourceVWConfig, err := wildwestclientset.NewForConfig(user1CachedResourceVWCfg)
	require.NoError(t, err)

	t.Logf("=== get ===")
	{
		t.Logf("Verify that user-1 cannot GET")
		_, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
			Get(ctx, "cowboy", metav1.GetOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 GET access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(consumerPath), "user-1-vw-get", "user-1", "User",
			[]string{"get"}, wildwestv1alpha1.SchemeGroupVersion.Group, "cowboys", "")

		t.Logf("Verify that user-1 can now get cowboy")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var err error
			_, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
				Get(ctx, cowboyName1, metav1.GetOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			if apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("waiting until the cowboy is in cache: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys")
	}

	/*
		t.Logf("Verify that user-1 can now wildcard list cowboys")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			cbs, err := wwUser1APIExportVWConfig.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			require.Len(t, cbs.Items, 1, "expected to find exactly one cowboy")
			cowboy = &cbs.Items[0]
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys")

		t.Logf("Verify that user-1 can now list cowboys")
		cbs, err := wwUser1APIExportVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, cbs.Items, 1, "expected to find exactly one cowboy")*/

	t.Logf("Create another cowboy CR")
	cowboyLabels := map[string]string{"hello": "world"}
	cowboyName2 := createCowboyInConsumer(ctx, t, consumerPath, wildwestClusterClient, cowboyLabels)
	setLabelsOnCowboyInConsumer(ctx, t, consumerPath, wildwestClusterClient, cowboyName2, cowboyLabels)

	t.Logf("=== list ===")
	{
		t.Logf("Verify that user-1 cannot LIST")
		_, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
			List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 LIST access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(consumerPath), "user-1-vw-list", "user-1", "User",
			[]string{"list"}, wildwestv1alpha1.SchemeGroupVersion.Group, "cowboys", "")

		t.Logf("Verify that user-1 can now LIST cowboys")
		var cowboyList *wildwestv1alpha1.CowboyList
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var err error
			cowboyList, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
				List(ctx, metav1.ListOptions{})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			if len(cowboyList.Items) < 2 {
				return false, fmt.Sprintf("waiting until there are two items in list, have %d", len(cowboyList.Items))
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys")
		require.Len(t, cowboyList.Items, 2, "exptected to find exactly two cowboys")

		t.Logf("Verify that both of the created cowboys are listed")
		cowboyNames := sets.NewString()
		for i := range cowboyList.Items {
			cowboyNames.Insert(cowboyList.Items[i].Name)
		}
		require.True(t, cowboyNames.HasAll(cowboyName1, cowboyName2),
			"expected the list to contain both cowboys, %s and %s, have %s", cowboyName1, cowboyName2, cowboyNames.List())
	}

	t.Logf("=== watch ===")
	{
		t.Logf("Verify that user-1 cannot WATCH")
		_, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").
			Watch(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err))

		t.Logf("Give user-1 WATCH access to the virtual workspace")
		admit(t, kubeClusterClient.Cluster(consumerPath), "user-1-vw-watch", "user-1", "User",
			[]string{"watch"}, wildwestv1alpha1.SchemeGroupVersion.Group, "cowboys", "")

		t.Logf("Verify that user-1 can now WATCH cowboys")
		var cowboyWatch watch.Interface
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var err error
			cowboyWatch, err = wwUser1CachedResourceVWConfig.Cluster(consumerClusterName.Path()).WildwestV1alpha1().Cowboys("default").Watch(ctx, metav1.ListOptions{
				LabelSelector: labels.SelectorFromSet(labels.Set(cowboyLabels)).String(),
			})
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("waiting until rbac cache is primed: %v", err)
			}
			require.NoError(t, err)
			return true, ""
		}, wait.ForeverTestTimeout, time.Millisecond*100, "expected user-1 to list cowboys")

		cowboyWatchCh := cowboyWatch.ResultChan()
		waitForEvent := func() (watch.Event, bool) {
			var event watch.Event
			var more bool
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				event, more = <-cowboyWatchCh
				return true, ""
			}, time.Second*2, time.Millisecond*100, "expected to get a watch event")
			return event, more
		}
		checkEvent := func(actualEvent watch.Event,
			expectedEventType watch.EventType,
			expectedNext, actualNext bool,
			checkCowboy func(cowboy *wildwestv1alpha1.Cowboy),
		) {
			require.Equal(t, expectedNext, actualNext, "unexpected channel state")
			if !expectedNext {
				// We don't expect any more events, nothing to check anymore.
				return
			}

			require.Equal(t, expectedEventType, actualEvent.Type, "unexpected event type")

			if checkCowboy != nil {
				cowboy := actualEvent.Object.(*wildwestv1alpha1.Cowboy)
				checkCowboy(cowboy)
			}
		}

		t.Logf("Set labels on first cowboy to %v", cowboyLabels)
		setLabelsOnCowboyInConsumer(ctx, t, consumerPath, wildwestClusterClient, cowboyName1, cowboyLabels)
		t.Logf("Verify that the second watched event is the first cowboy with updated labels %v", cowboyLabels)
		e, next := waitForEvent()
		checkEvent(e, watch.Modified, true, next, func(cowboy *wildwestv1alpha1.Cowboy) {
			require.Equal(t, cowboyName1, cowboy.Name, "expected to receive the first cowboy")
			require.Equal(t, cowboyLabels, cowboy.GetLabels(), "expected the cowboy to have labels defined")
		})

		t.Logf("Verify that stopping the watch works")
		cowboyWatch.Stop()
		e, next = waitForEvent()
		checkEvent(e, watch.Error, false, next, nil)
	}
}

func setUpServiceProvider(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClients kcpclientset.ClusterInterface, multipleVersions bool, serviceProviderWorkspace logicalcluster.Path, cfg *rest.Config, claims []apisv1alpha2.PermissionClaim) {
	t.Helper()
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	apiResPath := "apiresourceschema_cowboys.yaml"
	if multipleVersions {
		apiResPath = "apiresourceschema_cowboys_versions.yaml"
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, apiResPath, testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
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
			PermissionClaims: claims,
		},
	}
	_, err = kcpClients.Cluster(serviceProviderWorkspace).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func bindConsumerToProvider(ctx context.Context, t *testing.T, consumerWorkspace logicalcluster.Path, providerPath logicalcluster.Path, kcpClients kcpclientset.ClusterInterface, cfg *rest.Config, permissionClaims ...apisv1alpha2.AcceptablePermissionClaim) {
	t.Helper()
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
			PermissionClaims: permissionClaims,
		},
	}

	consumerWsClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClients.Cluster(consumerWorkspace).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := consumerWsClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
	resources, err := consumerWsClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
}

func createCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface, labels map[string]string) string {
	t.Helper()

	cowboyClusterClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	t.Logf("Create a cowboy CR in consumer workspace %q", consumer1Workspace)
	cowboy, err := cowboyClusterClient.Create(ctx, newCowboy("default", labels), metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in consumer workspace %q", consumer1Workspace)

	return cowboy.Name
}

func setLabelsOnCowboyInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, wildwestClusterClient wildwestclientset.ClusterInterface, name string, labels map[string]string) {
	t.Helper()

	cowboyClusterClient := wildwestClusterClient.Cluster(consumer1Workspace).WildwestV1alpha1().Cowboys("default")
	t.Logf("Update a cowboy CR in consumer workspace %q with labels %v", consumer1Workspace, labels)
	cowboy, err := cowboyClusterClient.Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err, "error getting cowboy %s in default namespace in consumer workspace %q", name, consumer1Workspace)

	cowboy.ObjectMeta.SetLabels(labels)
	_, err = cowboyClusterClient.Update(ctx, cowboy, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating cowboy %s in default namespace in consumer workspace %q", name, consumer1Workspace)
}

func createCachedResourceAndCachedResourceEndpointSliceInConsumer(ctx context.Context, t *testing.T, consumer1Workspace logicalcluster.Path, kcpClusterClient kcpclientset.ClusterInterface, cowboyName string) {
	t.Helper()

	cachedResourcesClient := kcpClusterClient.Cluster(consumer1Workspace).CacheV1alpha1().CachedResources()
	cachedResourceEndpointSlicesClient := kcpClusterClient.Cluster(consumer1Workspace).CacheV1alpha1().CachedResourceEndpointSlices()

	t.Logf("Make sure there are no CachedResourceEndpointSlices in consumer workspace %q at the beginning of the test", consumer1Workspace)
	var slices *cachev1alpha1.CachedResourceEndpointSliceList
	require.Eventually(t, func() bool {
		var err error
		slices, err = cachedResourceEndpointSlicesClient.List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list")
	require.Zero(t, len(slices.Items), "expected 0 CachedResourceEndpointSlices inside consumer workspace %q", consumer1Workspace)

	t.Logf("Make sure there are no CachedResources in consumer workspace %q at the beginning of the test", consumer1Workspace)
	var cachedResources *cachev1alpha1.CachedResourceList
	require.Eventually(t, func() bool {
		var err error
		cachedResources, err = kcpClusterClient.Cluster(consumer1Workspace).CacheV1alpha1().CachedResources().List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to be able to list")
	require.Zero(t, len(cachedResources.Items), "expected 0 CachedResourceEndpointSlices inside consumer workspace %q", consumer1Workspace)

	t.Logf("Create and wait for a CachedResource to cache Cowboys in consumer workspace %q", consumer1Workspace)
	cowboysCachedResource := newCachedResourceForCowboy("cowboys")
	_, err := cachedResourcesClient.Create(ctx, cowboysCachedResource, metav1.CreateOptions{})
	require.NoError(t, err, "error creating CachedResource in consumer workspace %q", consumer1Workspace)
	require.Eventually(t, func() bool {
		var err error
		cowboysCachedResource, err = cachedResourcesClient.Get(ctx, "cowboys", metav1.GetOptions{})
		return err == nil && cowboysCachedResource.Status.ResourceCounts != nil && cowboysCachedResource.Status.ResourceCounts.Local > 0 && cowboysCachedResource.Status.ResourceCounts.Cache > 0
	}, wait.ForeverTestTimeout, 100*time.Millisecond,
		"expected to have CachedResource with non-zero resource count in consumer workspace %q", consumer1Workspace)
	require.EqualValues(t, 1, cowboysCachedResource.Status.ResourceCounts.Local,
		"expected to have exactly one Cowboy object in consumer workspace %q to be collected by CachedResource", consumer1Workspace)
	require.EqualValues(t, 1, cowboysCachedResource.Status.ResourceCounts.Cache,
		"expected to have exactly one CachedObject created for cowboy CachedResource in consumer workspace %q", consumer1Workspace)

	t.Logf("Make sure exactly one CachedResourceEndpointSlice was automatically created as a result of creating a CachedResource in consumer workspace %q", consumer1Workspace)
	require.Eventually(t, func() bool {
		var err error
		slices, err = cachedResourceEndpointSlicesClient.List(ctx, metav1.ListOptions{})
		return err == nil && len(slices.Items) > 0
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected to have a non-empty list of CachedResourceEndpointSlices")
	require.EqualValues(t, 1, len(slices.Items), "expected exactly one CachedResourceEndpointSlices inside consumer workspace %q", consumer1Workspace)
}

func newCachedResourceForCowboy(name string) *cachev1alpha1.CachedResource {
	return &cachev1alpha1.CachedResource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
			Kind:       "CachedResource",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: cachev1alpha1.CachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    wildwestv1alpha1.SchemeGroupVersion.Group,
				Version:  wildwestv1alpha1.SchemeGroupVersion.Version,
				Resource: "cowboys",
			},
		},
	}
}

func newCowboy(namespace string, labels map[string]string) *wildwestv1alpha1.Cowboy {
	return &wildwestv1alpha1.Cowboy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: wildwestv1alpha1.SchemeGroupVersion.String(),
			Kind:       "Cowboy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    namespace,
			GenerateName: "cowboy-",
			Labels:       labels,
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "whoa!",
		},
	}
}

func createClusterRoleAndBindings(name, subjectName, subjectKind string, verbs []string, resources ...string) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	var total = len(resources) / 3

	rules := make([]rbacv1.PolicyRule, total)

	for i := range total {
		group := resources[i*3]
		resource := resources[i*3+1]
		resourceName := resources[i*3+2]

		r := rbacv1.PolicyRule{
			Verbs:     verbs,
			APIGroups: []string{group},
			Resources: []string{resource},
		}

		if resourceName != "" {
			r.ResourceNames = []string{resourceName}
		}

		rules[i] = r
	}

	return &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Rules: rules,
		}, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: subjectKind,
					Name: subjectName,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.SchemeGroupVersion.Group,
				Kind:     "ClusterRole",
				Name:     name,
			},
		}
}

func admit(t *testing.T, kubeClusterClient kubernetesclientset.Interface, ruleName, subjectName, subjectKind string, verbs []string, resources ...string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cr, crb := createClusterRoleAndBindings(ruleName, subjectName, subjectKind, verbs, resources...)
	_, err := kubeClusterClient.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
}
