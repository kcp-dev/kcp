package cachedresources

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	// corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Areas:
// * Permissions:
//   - Configure a user who shouldn't have access to the resources
//   - Add the perms, test that access works
//   - Validate that RO verbs work, RW verbs don't
// * Binding:
//   - Simple APIExport with a VR
//   - Mixed: custom and virtual resources
//   - Compare returned data
//   - Check OpenAPI
// * Virtual workspace:
//   - Verify that all of this ^^ works with APIExport VW

const XXX_timeout = math.MaxInt64

func TestCachedResources(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumers := []logicalcluster.Path{
		consumer1Path,
		consumer2Path,
	}

	t.Logf("providerPath: %v", providerPath)
	t.Logf("consumer1Path: %v", consumer1Path)
	t.Logf("consumer2Path: %v", consumer2Path)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	//
	// Create clients.
	//

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kcpCRDClusterClient := kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	wildwestClusterClient, err := wildwestclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	_ = kubeClusterClient
	_ = wildwestClusterClient

	//
	// Prepare the provider cluster.
	//

	// Prepare wildwest.dev resources "cowboys" and "sheriffs":
	// * Create CRD and generate its associated APIResourceSchema to be used with an APIExport later.
	// * Create a CachedResource for that resource and wait until it's ready.
	cachedResourceIdentities := map[string]string{
		"cowboys":  "",
		"sheriffs": "",
	}
	for resourceName := range cachedResourceIdentities {
		gr := metav1.GroupResource{
			Group:    "wildwest.dev",
			Resource: resourceName,
		}

		t.Logf("Creating %s.wildwest.dev CRD in %q", resourceName, providerPath)
		wildwest.Create(t, providerPath, kcpCRDClusterClient, gr)

		crd := wildwest.CRD(t, gr)
		sch, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "today")
		require.NoError(t, err)

		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(ctx, sch, metav1.CreateOptions{})
		require.NoError(t, err)

		// Create a CachedResource for that CR.
		t.Logf("Creating CachedResource for %s.v1alpha1.wildwest.dev in %q", resourceName, providerPath)
		cachedResource, err := kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResources().Create(ctx, &cachev1alpha1.CachedResource{
			ObjectMeta: metav1.ObjectMeta{
				Name: gr.String(),
			},
			Spec: cachev1alpha1.CachedResourceSpec{
				GroupVersionResource: cachev1alpha1.GroupVersionResource{
					Group:    "wildwest.dev",
					Version:  "v1alpha1",
					Resource: resourceName,
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			cachedResource, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResources().Get(ctx, cachedResource.Name, metav1.GetOptions{})
			return cachedResource, err
		}, kcptestinghelpers.Is(cachev1alpha1.ReplicationStarted), fmt.Sprintf("CachedResource %v should become ready", cachedResource.Name))

		cachedResourceIdentities[resourceName] = cachedResource.Status.IdentityHash
	}

	// Create an APIExport for the created CachedResources.
	t.Logf("Creating APIExport for wildwest.dev CachedResources in %q", providerPath)
	apiExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cached-wildwest-provider",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
							Group:        cachev1alpha1.SchemeGroupVersion.Group,
							Version:      cachev1alpha1.SchemeGroupVersion.Version,
							Resource:     "cachedresourceendpointslices",
							Name:         "cowboys.wildwest.dev",
							IdentityHash: cachedResourceIdentities["cowboys"],
						},
					},
				},
				{
					Group:  "wildwest.dev",
					Name:   "sheriffs",
					Schema: "today.sheriffs.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
							Group:        cachev1alpha1.SchemeGroupVersion.Group,
							Version:      cachev1alpha1.SchemeGroupVersion.Version,
							Resource:     "cachedresourceendpointslices",
							Name:         "sheriffs.wildwest.dev",
							IdentityHash: cachedResourceIdentities["sheriffs"],
						},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	//
	// Prepare the consumer clusters.
	//

	// Bind that APIExport in consumer workspaces and wait until the APIs are available and check that OpenAPI works too.
	for _, consumerPath := range consumers {
		t.Logf("Binding %s|%s in %s", providerPath, apiExport.Name, consumerPath)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cached-wildwest",
				},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: providerPath.String(),
							Name: apiExport.Name,
						},
					},
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			return err == nil, fmt.Sprintf("failed to create APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Second*1, "waiting to create apibinding")

		for resourceName := range cachedResourceIdentities {
			t.Logf("Waiting for %s.v1alpha1.wildwest.dev API to appear in %q", resourceName, consumerPath)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				resourcesList, err := kcpClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
				if err != nil {
					return false, fmt.Sprintf("failed to retrieve APIResourceList from discovery: %v", err)
				}
				return slices.ContainsFunc(resourcesList.APIResources, func(e metav1.APIResource) bool {
					return e.Group == wildwestv1alpha1.SchemeGroupVersion.Group &&
						e.Version == wildwestv1alpha1.SchemeGroupVersion.Version &&
						e.Name == resourceName
				}), fmt.Sprintf("%s.v1alpha1.wildwest.dev API not found in %q", resourceName, consumerPath)
			}, wait.ForeverTestTimeout, time.Second*1, "waiting for %s.v1alpha1.wildwest.dev API in %q", resourceName, consumerPath)

			t.Logf("Ensure %s.v1alpha1.wildwest.dev API is available in OpenAPIv3 endpoint in %q", resourceName, consumerPath)
			paths, err := kcpClusterClient.Cluster(consumerPath).Discovery().OpenAPIV3().Paths()
			require.NoError(t, err, "error retrieving OpenAPIv3 paths in %q", consumerPath)
			resourcePath := fmt.Sprintf("apis/%s", wildwestv1alpha1.SchemeGroupVersion.String())
			require.Contains(t, paths, resourcePath, "OpenAPIv3 paths should include %q in %q", resourcePath, consumerPath)
		}
	}

	// Get clients for the APIExport's VW.

	//
	// Verify.
	//

	// 1. List in consumers, make sure nothing shows up.
	// 2. Create a watch in consumer, wait until an event appears.
	// 3. Create a resource in provider, consumer should see one obj, close watch.
	// 4.

	for _, consumerPath := range consumers {
		t.Logf("Ensure that there are no Cowboys in %q", consumerPath)
		cowboysList, err := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Empty(t, cowboysList.Items, "there should be no Cowboy objects at this point in time")

		/*t.Logf("Ensure that there are no Sheriffs in %q", consumerPath)
		sheriffsList, err := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Sherifves().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Empty(t, sheriffsList.Items, "there should be no Sheriff objects at this point in time")*/
	}

	t.Logf("Starting wildcard watch for Cowboys")
	watchCowboys, err := wildwestClusterClient.WildwestV1alpha1().Cowboys().Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchCowboys.Stop()

	/*t.Logf("Starting wildcard watch for Sheriffs")
	watchSheriffs, err := wildwestClusterClient.WildwestV1alpha1().Sherifves().Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchSheriffs.Stop()*/

	incOnAdded := func(counter *int32, w watch.Interface) {
		for {
			select {
			case e, more := <-w.ResultChan():
				if !more {
					return
				}
				if e.Type == watch.Added {
					atomic.AddInt32(counter, 1)
				}
			case <-ctx.Done():
				w.Stop()
				return
			}
		}
	}

	var seenCowboys /*, seenSheriffs*/ int32
	go incOnAdded(&seenCowboys, watchCowboys)
	/*go incOnAdded(&seenSheriffs, watchSheriffs)*/

	t.Logf("Creating a Cowboy in %q", providerPath)
	_, err = wildwestClusterClient.Cluster(providerPath).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboy-1",
		},
	}, metav1.CreateOptions{})

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		count := atomic.LoadInt32(&seenCowboys)
		return count == 3, fmt.Sprintf("expected to see 3 Cowboys, got %d", count)
	}, wait.ForeverTestTimeout, time.Second*1, "waiting for three cowboys")

	/*kcptestinghelpers.Eventually(t, func() (bool, string) {
		count := atomic.LoadInt32(&seenSheriffs)
		return count == 3, fmt.Sprintf("expected to see 3 Sheriffs, got %d", count)
	}, wait.ForeverTestTimeout, time.Second*1, "waiting for three sheriffs")*/

	/*t.Logf("Make sure cowboys API group does NOT show up in workspace %q openapi v3 endpoint", providerPath)
	providerOpenAPIV3 := providerWorkspaceClient.Cluster(providerPath).Discovery().OpenAPIV3()
	paths, err := providerOpenAPIV3.Paths()
	require.NoError(t, err, "error retrieving %q openapi v3 paths", providerPath)
	require.NotContainsf(t, paths, "apis/"+wildwestv1alpha1.SchemeGroupVersion.String(), "should not have %s API group in %q openapi v3 paths", wildwest.GroupName, providerPath)*/
}
