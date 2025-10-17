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

package cachedresources

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	tenancy1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCachedResources(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer1Path, consumer1WS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer2Path, consumer2WS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPaths := sets.New(logicalcluster.Name(consumer1WS.Spec.Cluster).Path(), logicalcluster.Name(consumer2WS.Spec.Cluster).Path())
	consumerWorkspaces := map[logicalcluster.Path]*tenancy1alpha1.Workspace{
		consumer1Path: consumer1WS,
		consumer2Path: consumer2WS,
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

	kcpDynClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp dynamic cluster client for server")

	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kcpCRDClusterClient := kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	wildwestClusterClient, err := wildwestclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	//
	// Prepare the provider cluster.
	//

	resourceNames := sets.New("cowboys", "sheriffs")

	// Prepare wildwest.dev resources "cowboys" and "sheriffs":
	// * Create CRD and generate its associated APIResourceSchema to be used with an APIExport later.
	// * Create a CachedResource for that resource and wait until it's ready.
	cachedResourceIdentities := make(map[string]string)
	for resourceName := range resourceNames {
		gr := metav1.GroupResource{
			Group:    "wildwest.dev",
			Resource: resourceName,
		}

		t.Logf("Creating %s.wildwest.dev CRD in %q", resourceName, providerPath)
		wildwest.Create(t, providerPath, kcpCRDClusterClient, gr)

		crd := wildwest.CRD(t, gr)
		sch, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "today")
		require.NoError(t, err)

		t.Logf("Creating %s APIResourceSchema in %q", sch.Name, providerPath)
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
							Reference: corev1.TypedLocalObjectReference{
								APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
								Kind:     "CachedResourceEndpointSlice",
								Name:     "cowboys.wildwest.dev",
							},
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
							Reference: corev1.TypedLocalObjectReference{
								APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
								Kind:     "CachedResourceEndpointSlice",
								Name:     "sheriffs.wildwest.dev",
							},
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
	for consumerPath := range consumerPaths {
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
			if err != nil {
				return false, fmt.Sprintf("failed to create APIBinding: %v", err)
			}
			return true, ""
		}, wait.ForeverTestTimeout, time.Second*1, "waiting to create apibinding")

		for resourceName := range resourceNames {
			t.Logf("Waiting for %s.v1alpha1.wildwest.dev API to appear in %q", resourceName, consumerPath)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				groupList, err := kcpClusterClient.Cluster(consumerPath).Discovery().ServerGroups()
				if err != nil {
					return false, fmt.Sprintf("failed to retrieve APIResourceList from discovery: %v", err)
				}
				return slices.ContainsFunc(groupList.Groups, func(e metav1.APIGroup) bool {
					return e.Name == wildwestv1alpha1.SchemeGroupVersion.Group
				}), fmt.Sprintf("wildwest.dev group not found in %q", consumerPath)
			}, wait.ForeverTestTimeout, time.Second*1, "waiting for wildwest.dev group in %q", consumerPath)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				resourceList, err := kcpClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion("wildwest.dev/v1alpha1")
				if err != nil {
					return false, fmt.Sprintf("failed to retrieve APIResourceList from discovery: %v", err)
				}
				return slices.ContainsFunc(resourceList.APIResources, func(e metav1.APIResource) bool {
					return e.Name == resourceName
				}), fmt.Sprintf("%s.v1alpha1.wildwest.dev API not found in %q", resourceName, consumerPath)
			}, wait.ForeverTestTimeout, time.Second*1, "waiting for wildwest.dev group in %q", resourceName, consumerPath)

			t.Logf("Ensure %s.v1alpha1.wildwest.dev API is available in OpenAPIv3 endpoint in %q", resourceName, consumerPath)
			paths, err := kcpClusterClient.Cluster(consumerPath).Discovery().OpenAPIV3().Paths()
			require.NoError(t, err, "error retrieving OpenAPIv3 paths in %q", consumerPath)
			resourcePath := fmt.Sprintf("apis/%s", wildwestv1alpha1.SchemeGroupVersion.String())
			require.Contains(t, paths, resourcePath, "OpenAPIv3 paths should include %q in %q", resourcePath, consumerPath)
		}
	}

	// Map of Host->{Admissible workspaces}.
	// A client for a certain host (e.g. a virtual workspace) may be able to reach only
	// workspaces co-located on the same shard. This map holds these associations.
	admissibleWorkspaces := make(map[string]sets.Set[logicalcluster.Path])
	// The default `cfg` is configured with the external addr, so both
	// workspaces are reachable regardless of which shard they are on.
	admissibleWorkspaces[cfg.Host] = sets.New(consumer1Path, consumer2Path)

	// Generate APIExport VW client configs. The consumers could each be in a different
	// shard, and so we need to wait until (possibly) both appear in APIExportEndpointSlice's
	// endpoint URLs.
	apiExportVWClientConfigs := make(map[string]*rest.Config)
	for consumerPath, consumerWS := range consumerWorkspaces {
		t.Logf("Creating APIExport VW client for consumer in %q", consumerPath)

		var vwURL string
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			// First, we get the APIExportEndpointSlice, and then we extract the URL that fits the consumer's workspace host, i.e. the shard URL.

			apiExportEndpointSlice, err := kcpClusterClient.ApisV1alpha1().Cluster(providerPath).APIExportEndpointSlices().Get(ctx, apiExport.Name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}

			var found bool
			vwURL, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumerWS,
				framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
			if err != nil {
				return false, err.Error()
			}
			return found, fmt.Sprintf("URL for workspace %q not found in APIExportEndpointSlice %s|%s", consumerPath, providerPath, apiExport.Name)
		}, wait.ForeverTestTimeout, time.Second*1, "waiting for three cowboys")

		if _, ok := apiExportVWClientConfigs[vwURL]; !ok {
			vwCfg := rest.CopyConfig(cfg)
			vwCfg.Host = vwURL
			apiExportVWClientConfigs[vwURL] = vwCfg
		}

		if _, ok := admissibleWorkspaces[vwURL]; !ok {
			admissibleWorkspaces[vwURL] = make(sets.Set[logicalcluster.Path])
		}
		admissibleWorkspaces[vwURL].Insert(consumerPath)
	}

	//
	// Verify.
	//

	// Make sure there are no cowboys or sheriffs in either of the consumer workspaces at the beginning.
	for consumerPath := range consumerPaths {
		t.Logf("Ensure that there are no Cowboys in %q", consumerPath)
		cowboysList, err := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Empty(t, cowboysList.Items, "there should be no Cowboy objects at this point in time")

		t.Logf("Ensure that there are no Sheriffs in %q", consumerPath)
		sheriffsList, err := listSheriffs(ctx, kcpDynClusterClient, logicalcluster.Name(consumerPath.String()))
		require.NoError(t, err)
		require.Empty(t, sheriffsList.Items, "there should be no Sheriff objects at this point in time")
	}

	t.Log("Verify watching and wildcards work")

	resourceCounters := map[string]*int32{
		"cowboys":  ptr.To[int32](0),
		"sheriffs": ptr.To[int32](0),
	}
	var watchStopFuncs []func()
	defer func() {
		for _, stop := range watchStopFuncs {
			stop()
		}
	}()

	// Create a watch for each resource we're interested in (Cowboys and Sheriffs)
	// against each APIExport VW endpoint we've found above.
	for resourceName, counter := range resourceCounters {
		for _, cfg := range apiExportVWClientConfigs {
			dynClient, err := kcpdynamic.NewForConfig(cfg)
			require.NoError(t, err)

			t.Logf("Creating a wildcard watch for %s against %q", resourceName, cfg.Host)

			w, err := dynClient.Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName)).
				Watch(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			resultCh := w.ResultChan()

			// We'll increment the counter each time we see an Added event.

			go func() {
				for {
					select {
					case e, more := <-resultCh:
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
			}()

			watchStopFuncs = append(watchStopFuncs, w.Stop)
		}
	}

	// Create one Cowboy and one Sheriff in the provider workspace.
	// Eventually we should see two of each in the resourcesCounter,
	// i.e. one for each consumer workspace.
	t.Logf("Creating a Cowboy in %q", providerPath)
	cowboyOne, err := wildwestClusterClient.Cluster(providerPath).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys-1",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Creating a Sheriff in %q", providerPath)
	sheriffOne, err := createSheriff(ctx, kcpDynClusterClient, logicalcluster.Name(providerPath.String()), &wildwestv1alpha1.Sheriff{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-1",
		},
	})
	require.NoError(t, err)

	// Finally wait for the counters to be updated, and equal to two.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		for resourceName, counter := range resourceCounters {
			count := atomic.LoadInt32(counter)
			if count != 2 {
				return false, fmt.Sprintf("expected to see 2 %s from wildcard watches, got %d", resourceName, count)
			} else {
				t.Logf("Seen 2 %s from wildcard watches", resourceName)
			}
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Second*1, "waiting for two cowboys and two sheriffs")

	t.Log("Stopping watches")
	for _, stop := range watchStopFuncs {
		// Intentionally run stop() twice. Watches shouldn't panic because of that.
		stop()
		stop()
	}

	// Make sure listing * for Cowboys and Sheriffs returns two items for each.
	for resourceName := range resourceNames {
		counter := 0
		for _, cfg := range apiExportVWClientConfigs {
			dynClient, err := kcpdynamic.NewForConfig(cfg)
			require.NoError(t, err)

			t.Logf("Wildcard listing for %s against %q should return two items", resourceName, cfg.Host)

			list, err := dynClient.Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName)).
				List(ctx, metav1.ListOptions{})
			require.NoError(t, err)

			counter += len(list.Items)
		}

		require.Equal(t, 2, counter, "Wildcard listing should return two %s", resourceName)
	}

	// Make sure listing and getting resources in a specific cluster works, both through
	// APIExport VW and regular workspace, and that it has expected contents.
	wildwestResourceNamespaces := map[string]string{
		cowboyOne.Name:  cowboyOne.Namespace,
		sheriffOne.Name: sheriffOne.Namespace, // Actually, no namespace here -- this is just for the consistency's sake
	}
	namespaceableResource := func(namespace string, nsRes dynamic.NamespaceableResourceInterface) dynamic.ResourceInterface {
		if namespace == "" {
			return nsRes
		}
		return nsRes.Namespace(namespace)
	}
	// Get all dynamic clients in a single slice so that we can do this all in one go.
	cfgs := make([]*rest.Config, 0, len(apiExportVWClientConfigs))
	for _, c := range apiExportVWClientConfigs {
		cfgs = append(cfgs, c)
	}
	dynClients := make(map[string]kcpdynamic.ClusterInterface)
	for _, cfg := range append(cfgs, cfg) {
		dynClient, err := kcpdynamic.NewForConfig(cfg)
		require.NoError(t, err)
		dynClients[cfg.Host] = dynClient
	}
	for resourceName := range resourceNames {
		// We'll need these for object comparison below.
		cowboyOneNormalized := cowboyOne.DeepCopy()
		cowboyOneNormalized.APIVersion = wildwestv1alpha1.SchemeGroupVersion.String()
		cowboyOneNormalized.Kind = "Cowboy"
		cowboyOneNormalized.Annotations = make(map[string]string)

		sheriffOneNormalized := sheriffOne.DeepCopy()
		sheriffOneNormalized.APIVersion = wildwestv1alpha1.SchemeGroupVersion.String()
		sheriffOneNormalized.Kind = "Sheriff"
		sheriffOneNormalized.Annotations = make(map[string]string)

		for consumerPath, consumerWS := range consumerWorkspaces {
			// Set the cluster name to the expected values.
			cowboyOneNormalized.Annotations[logicalcluster.AnnotationKey] = consumerWS.Spec.Cluster
			sheriffOneNormalized.Annotations[logicalcluster.AnnotationKey] = consumerWS.Spec.Cluster

			cowboyOneNormalizedUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cowboyOneNormalized)
			require.NoError(t, err)

			sheriffOneNormalizedUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sheriffOneNormalized)
			require.NoError(t, err)

			wildwestObjsNormalizedUnstructured := map[string]map[string]interface{}{
				cowboyOneNormalized.Name:  normalizeUnstructuredMap(cowboyOneNormalizedUnstructured),
				sheriffOneNormalized.Name: normalizeUnstructuredMap(sheriffOneNormalizedUnstructured),
			}

			for host, dynClient := range dynClients {
				if !admissibleWorkspaces[host].Has(consumerPath) {
					continue
				}

				objName := resourceName + "-1"
				objNamespace := wildwestResourceNamespaces[objName]

				t.Logf("Listing %s resources in %q via %q should return one object", resourceName, consumerPath, host)
				list, err := namespaceableResource(
					objNamespace,
					dynClient.Cluster(logicalcluster.NewPath(consumerWS.Spec.Cluster)).
						Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName))).
					List(ctx, metav1.ListOptions{})
				require.NoError(t, err)
				require.Equal(t, 1, len(list.Items), "Unexpected number of items in %s list in %q when listing through %q", resourceName, consumerPath, host)

				require.NoError(t, err)
				require.EqualValues(t, wildwestObjsNormalizedUnstructured[objName], normalizeUnstructuredMap(list.Items[0].Object))

				t.Logf("Getting a %s resource named %s in %q via %q should return that object", resourceName, objName, consumerPath, host)
				obj, err := namespaceableResource(
					objNamespace,
					dynClient.Cluster(logicalcluster.NewPath(consumerWS.Spec.Cluster)).
						Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName))).
					Get(ctx, objName, metav1.GetOptions{})
				require.NoError(t, err)
				require.EqualValues(t, wildwestObjsNormalizedUnstructured[objName], normalizeUnstructuredMap(obj.Object))
			}
		}
	}

	// Verify that APIBinding's conflict checker blocks creating Sheriff CRD in consumer1WS.
	t.Log("Creating a CRD with conflicting name")
	sheriffsCRDConflicting, err := kcpCRDClusterClient.Cluster(consumer1Path).Create(ctx, wildwest.CRD(t, metav1.GroupResource{
		Group:    wildwestv1alpha1.SchemeGroupVersion.Group,
		Resource: "sheriffs",
	}), metav1.CreateOptions{})
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sheriffsCRDConflicting, err = kcpApiExtensionClusterClient.Cluster(consumer1Path).ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "sheriffs.wildwest.dev", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get CRD: %v", err)
		}
		return apiextensionshelpers.IsCRDConditionFalse(sheriffsCRDConflicting, apiextensionsv1.NamesAccepted), "the CRD should not be accepted because of names collision"
	}, wait.ForeverTestTimeout, time.Second*1, "waiting to create apibinding")
}

func normalizeUnstructuredMap(origObj map[string]interface{}) map[string]interface{} {
	obj := maps.Clone(origObj)

	meta, hasMeta := obj["metadata"].(map[string]interface{})
	if hasMeta {
		delete(meta, "creationTimestamp")
		delete(meta, "resourceVersion")
		delete(meta, "selfLink")
		delete(meta, "uid")
		delete(meta, "generation")
		delete(meta, "managedFields")

		ann, hasAnn := meta["annotations"].(map[string]interface{})
		if hasAnn {
			// TODO(gman0): HACK! https://github.com/kcp-dev/kcp/issues/3478
			// Partial metadata objects have this annotation added.
			// This will go away once we have full objects.
			delete(ann, "kcp.io/original-api-version")
		}
	}

	// TODO(gman0): HACK! https://github.com/kcp-dev/kcp/issues/3478
	// We need to remove spec and status, because we're getting only metadata for now.
	// This will go away once we have full objects.
	delete(obj, "spec")
	delete(obj, "status")

	return obj
}

func listSheriffs(ctx context.Context, c kcpdynamic.ClusterInterface, cluster logicalcluster.Name) (*wildwestv1alpha1.SheriffList, error) {
	uList, err := c.Cluster(cluster.Path()).Resource(
		wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs"),
	).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var sheriffs wildwestv1alpha1.SheriffList
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(uList.UnstructuredContent(), &sheriffs)
	if err != nil {
		return nil, err
	}

	return &sheriffs, nil
}

func createSheriff(ctx context.Context, c kcpdynamic.ClusterInterface, cluster logicalcluster.Name, sheriff *wildwestv1alpha1.Sheriff) (*wildwestv1alpha1.Sheriff, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sheriff)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{
		Object: m,
	}
	u.SetAPIVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	u.SetKind("Sheriff")

	u, err = c.Cluster(cluster.Path()).Resource(
		wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs"),
	).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	createdSheriff := &wildwestv1alpha1.Sheriff{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, createdSheriff)
	return createdSheriff, err
}
