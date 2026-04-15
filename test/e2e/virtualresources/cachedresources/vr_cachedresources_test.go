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

package cachedresources

import (
	"context"
	"fmt"
	"maps"
	"reflect"
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
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	tenancy1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCachedResources(t *testing.T) {
	t.Skip("skipping for now because the test is not stable, see https://github.com/kcp-dev/kcp/issues/4026")

	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer1Path, consumer1WS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumer2Path, consumer2WS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPaths := sets.New(consumer1Path, consumer2Path)
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

	//
	// Prepare the provider cluster.
	//

	resourceNames := sets.New("sheriffs")

	// Prepare wildwest.dev resource "sheriffs":
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
		kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(providerPath).Discovery(), wildwestv1alpha1.SchemeGroupVersion)

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

	// Create CachedResourceEndpointSlices for the CachedResource(s) we've created above.
	for resourceName := range resourceNames {
		gr := metav1.GroupResource{
			Group:    "wildwest.dev",
			Resource: resourceName,
		}
		name := gr.String()

		// Create a CachedResourceEndpointSlice for the CachedResource ^^.
		t.Logf("Creating a CachedResourceEndpointSlice that references %s CachedResource and %s APIExport (will create right after) in %q", resourceName, apiExport.Name, providerPath)
		cres, err := kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResourceEndpointSlices().Create(ctx, &cachev1alpha1.CachedResourceEndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
				CachedResource: cachev1alpha1.CachedResourceReference{
					Name: name,
				},
				APIExport: cachev1alpha1.ExportBindingReference{
					Name: apiExport.Name,
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			cres, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResourceEndpointSlices().Get(ctx, cres.Name, metav1.GetOptions{})
			return cres, err
		}, kcptestinghelpers.Is(cachev1alpha1.CachedResourceValid), fmt.Sprintf("CachedResourceEndpointSlice %v should have its CachedResource reference valid", name))
		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			cres, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResourceEndpointSlices().Get(ctx, cres.Name, metav1.GetOptions{})
			return cres, err
		}, kcptestinghelpers.Is(cachev1alpha1.APIExportValid), fmt.Sprintf("CachedResourceEndpointSlice %v should have its APIExport reference valid", apiExport.Name))
	}

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

	// Generate a VW client config for each consumer workspace. Consumers may be on different
	// shards, so use framework.VirtualWorkspaceURL to find the correct shard-local endpoint.
	consumerVWConfigs := make(map[logicalcluster.Path]*rest.Config)
	for consumerPath, consumerWS := range consumerWorkspaces {
		t.Logf("Creating APIExport VW client for consumer in %q", consumerPath)

		var vwURL string
		kcptestinghelpers.Eventually(t, func() (bool, string) {
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
		}, wait.ForeverTestTimeout, time.Second*1, "waiting for workspace URL in APIExportEndpointSlice")

		vwCfg := rest.CopyConfig(cfg)
		vwCfg.Host = vwURL
		consumerVWConfigs[consumerPath] = vwCfg
	}

	//
	// Verify.
	//

	// Make sure there are no sheriffs in either of the consumer workspaces at the beginning.
	for consumerPath := range consumerPaths {
		t.Logf("Ensure that there are no Sheriffs in %q", consumerPath)
		sheriffsList, err := listSheriffs(ctx, kcpDynClusterClient, logicalcluster.Name(consumerWorkspaces[consumerPath].Spec.Cluster))
		require.NoError(t, err)
		require.Empty(t, sheriffsList.Items, "there should be no Sheriff objects at this point in time")
	}

	t.Log("Verify watching per-consumer workspace works")

	consumerCounters := make(map[logicalcluster.Path]*int32)
	for consumerPath := range consumerPaths {
		consumerCounters[consumerPath] = ptr.To[int32](0)
	}
	watchStopFuncs := make([]func(), 0, len(consumerWorkspaces))
	defer func() {
		for _, stop := range watchStopFuncs {
			stop()
		}
	}()

	// Create a watch for Sheriffs in each consumer workspace via its APIExport VW endpoint.
	for consumerPath, consumerWS := range consumerWorkspaces {
		counter := consumerCounters[consumerPath]
		require.NotNil(t, counter)

		dynClient, err := kcpdynamic.NewForConfig(consumerVWConfigs[consumerPath])
		require.NoError(t, err)

		t.Logf("Creating a watch for sheriffs in consumer workspace %q", consumerPath)
		w, err := dynClient.Cluster(logicalcluster.NewPath(consumerWS.Spec.Cluster)).
			Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs")).
			Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		resultCh := w.ResultChan()

		go func() {
			for {
				select {
				case e, more := <-resultCh:
					if !more {
						return
					}
					obj, ok := e.Object.(*unstructured.Unstructured)
					if !ok {
						continue
					}
					if obj.GetName() != "sheriffs-1" {
						continue
					}
					// The provider helper does a Create followed by UpdateStatus, so depending on
					// when the watch attaches the first visible event can be either Added or Modified.
					// We only care that each consumer watch observed the object at least once.
					if e.Type == watch.Added || e.Type == watch.Modified {
						atomic.CompareAndSwapInt32(counter, 0, 1)
					}
				case <-ctx.Done():
					w.Stop()
					return
				}
			}
		}()

		watchStopFuncs = append(watchStopFuncs, w.Stop)
	}

	// Create one Sheriff in the provider workspace.
	// Eventually we should see one Sheriff in each consumer workspace's watch.
	t.Logf("Creating a Sheriff in %q", providerPath)
	sheriffOne := createSheriff(t, ctx, kcpDynClusterClient, logicalcluster.Name(providerPath.String()), &wildwestv1alpha1.Sheriff{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-1",
		},
		Spec: wildwestv1alpha1.SheriffSpec{
			Intent: "sheriffs-1-spec",
		},
		Status: wildwestv1alpha1.SheriffStatus{
			Result: "sheriffs-1-status",
		},
	})
	require.NoError(t, err)

	// Wait for each consumer workspace to see the sheriff via its watch.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		for consumerPath, counter := range consumerCounters {
			count := atomic.LoadInt32(counter)
			if count != 1 {
				return false, fmt.Sprintf("expected to see 1 sheriff in %q from watch, got %d", consumerPath, count)
			} else {
				t.Logf("Seen 1 sheriff in %q from watch", consumerPath)
			}
		}
		return true, ""
	}, wait.ForeverTestTimeout*2, time.Millisecond*500, "waiting for one sheriff per consumer workspace")

	t.Log("Stopping watches")
	for _, stop := range watchStopFuncs {
		// Intentionally run stop() twice. Watches shouldn't panic because of that.
		stop()
		stop()
	}

	// Make sure listing and getting resources in a specific cluster works through the
	// APIExport VW and through the regular cluster-scoped client, and that results have expected contents.
	for resourceName := range resourceNames {
		sheriffOneNormalized := sheriffOne.DeepCopy()
		sheriffOneNormalized.APIVersion = wildwestv1alpha1.SchemeGroupVersion.String()
		sheriffOneNormalized.Kind = "Sheriff"
		sheriffOneNormalized.Annotations = make(map[string]string)

		for consumerPath, consumerWS := range consumerWorkspaces {
			consumerCluster := logicalcluster.Name(consumerWS.Spec.Cluster)
			objName := resourceName + "-1"

			sheriffOneNormalized.Annotations[logicalcluster.AnnotationKey] = consumerWS.Spec.Cluster
			sheriffOneNormalizedUnstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sheriffOneNormalized)
			require.NoError(t, err)

			expected := normalizeUnstructuredMap(sheriffOneNormalizedUnstructured)

			verifyListAndGet(t, ctx, consumerVWConfigs[consumerPath], consumerCluster, resourceName, objName, expected)
			verifyListAndGet(t, ctx, cfg, consumerCluster, resourceName, objName, expected)
		}
	}

	// Verify that APIBinding's conflict checker blocks creating Sheriff CRD in consumer1WS.
	t.Log("Creating a CRD with conflicting name, it should NOT be accepted")
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

func verifyListAndGet(
	t *testing.T,
	ctx context.Context,
	cfg *rest.Config,
	targetCluster logicalcluster.Name,
	resourceName string,
	objName string,
	expected map[string]interface{},
) {
	t.Helper()

	dynClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Listing %s resources in %q via %q client should return one object", resourceName, targetCluster, cfg.Host)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := dynClient.Cluster(targetCluster.Path()).
			Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName)).
			List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list: %v", err)
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("expected 1 item, got %d", len(list.Items))
		}
		actual := normalizeUnstructuredMap(list.Items[0].Object)
		if !reflect.DeepEqual(actual, expected) {
			return false, fmt.Sprintf("list result mismatch:\n  expected: %v\n  actual:   %v", expected, actual)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "listing %s in %q via %q", resourceName, targetCluster, cfg.Host)

	t.Logf("Getting a %s resource named %s in %q via %q should return that object", resourceName, objName, targetCluster, cfg.Host)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		obj, err := dynClient.Cluster(targetCluster.Path()).
			Resource(wildwestv1alpha1.SchemeGroupVersion.WithResource(resourceName)).
			Get(ctx, objName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get: %v", err)
		}
		actual := normalizeUnstructuredMap(obj.Object)
		if !reflect.DeepEqual(actual, expected) {
			return false, fmt.Sprintf("get result mismatch:\n  expected: %v\n  actual:   %v", expected, actual)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*500, "getting %s/%s in %q via %q", resourceName, objName, targetCluster, cfg.Host)
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
	}

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

func createSheriff(t *testing.T, ctx context.Context, c kcpdynamic.ClusterInterface, cluster logicalcluster.Name, sheriff *wildwestv1alpha1.Sheriff) *wildwestv1alpha1.Sheriff {
	t.Helper()

	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(sheriff)
	require.NoError(t, err)
	u := &unstructured.Unstructured{
		Object: m,
	}
	u.SetAPIVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	u.SetKind("Sheriff")

	u, err = c.Cluster(cluster.Path()).Resource(
		wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs"),
	).Create(ctx, u, metav1.CreateOptions{})
	require.NoError(t, err)

	createdSheriff := &wildwestv1alpha1.Sheriff{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, createdSheriff)
	require.NoError(t, err)

	createdSheriff.Status = sheriff.Status
	m, err = runtime.DefaultUnstructuredConverter.ToUnstructured(createdSheriff)
	require.NoError(t, err)
	u.Object = m

	u, err = c.Cluster(cluster.Path()).Resource(
		wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs"),
	).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	require.NoError(t, err)

	sheriffWithStatus := &wildwestv1alpha1.Sheriff{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, sheriffWithStatus)
	require.NoError(t, err)

	require.Equal(t, sheriff.Spec, sheriffWithStatus.Spec, "created Sheriff should have Spec")
	require.Equal(t, sheriff.Status, sheriffWithStatus.Status, "created Sheriff should have Status")

	return sheriffWithStatus
}
