/*
Copyright 2022 The KCP Authors.

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

package cache

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	cache2e "github.com/kcp-dev/kcp/test/e2e/reconciler/cache"
)

// testSchemaIsNotEnforced checks if an object of any schema can be stored as "apis.kcp.dev.v1alpha1.apiexports"
func testSchemaIsNotEnforced(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	type planet struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata,omitempty"`
		Star              string `json:"spec,omitempty"`
		Size              int    `json:"size,omitempty"`
	}
	earth := planet{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}, Star: "TheSun", Size: 40000}
	validateFn := func(earth planet, cachedPlatenRaw *unstructured.Unstructured) {
		cachedEarthJson, err := cachedPlatenRaw.MarshalJSON()
		require.NoError(t, err)
		cachedEarth := &planet{}
		require.NoError(t, json.Unmarshal(cachedEarthJson, cachedEarth))

		earth.UID = cachedEarth.UID
		earth.Generation = cachedEarth.Generation
		earth.ResourceVersion = cachedEarth.ResourceVersion
		earth.CreationTimestamp = cachedEarth.CreationTimestamp
		earth.ResourceVersion = cachedEarth.ResourceVersion
		earth.Annotations = cachedEarth.Annotations
		if !cmp.Equal(cachedEarth, &earth) {
			t.Fatalf("received object from the cache server differs from the expected one:\n%s", cmp.Diff(cachedEarth, &earth))
		}
	}

	t.Logf("Create abmer|%s/earth (shard|cluster/name) on the cache server without type information", cluster)
	earthRaw, err := toUnstructured(&earth)
	require.NoError(t, err)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(cacheclient.WithShardInContext(ctx, shard.New("amber")), shard.New("amber")), earthRaw, metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected to receive an error when storing an object without providing TypeMeta")
	}

	earth.APIVersion = "apis.kcp.dev/v1alpha1"
	earth.Kind = "APIExport"
	t.Logf("Create abmer/%s/earth on the cache server without providing a name", cluster)
	earthRaw, err = toUnstructured(&earth)
	require.NoError(t, err)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(cacheclient.WithShardInContext(ctx, shard.New("amber")), shard.New("amber")), earthRaw, metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected to receive an error when storing an object without a name")
	}

	earth.Name = "earth"
	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, earth.Name)
	earthRaw, err = toUnstructured(&earth)
	require.NoError(t, err)
	cachedEarthRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(cacheclient.WithShardInContext(ctx, shard.New("amber")), shard.New("amber")), earthRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(earth, cachedEarthRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer|%s/%s (shard|cluster/name) from the cache server", cluster, earth.Name)
	cachedEarthRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(cacheclient.WithShardInContext(ctx, shard.New("amber")), shard.New("amber")), earth.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(earth, cachedEarthRaw)
}

// testShardNamesAssigned checks if a shard name is provided in the "kcp.dev/shard" annotation and
// if a cluster name is stored at "kcp.dev/cluster" annotation
func testShardClusterNamesAssigned(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialComicDB := newFakeAPIExport("comicdb")
	validateFn := func(cachedComicDBRaw *unstructured.Unstructured) {
		cachedComicDBJson, err := cachedComicDBRaw.MarshalJSON()
		require.NoError(t, err)
		cachedComicDB := &fakeAPIExport{}
		require.NoError(t, json.Unmarshal(cachedComicDBJson, cachedComicDB))
		if cachedComicDB.Annotations["kcp.dev/shard"] != "amber" {
			t.Fatalf("unexpected shard name %v assigned to cached amber|%s/%s (shard|cluster/name) , expected %s", cachedComicDB.Annotations["kcp.dev/shard"], cluster, cachedComicDB.Name, "amber")
		}
		if cachedComicDB.Annotations["kcp.dev/cluster"] != cluster.String() {
			t.Fatalf("unexpected cluster name %v assigned to cached amber|%s/%s (shard|cluster/name), expected %s", cachedComicDB.Annotations["kcp.dev/cluster"], cluster, cachedComicDB.Name, cluster.String())
		}
	}

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialComicDB.Name)
	comicDBRaw, err := toUnstructured(&initialComicDB)
	require.NoError(t, err)
	cachedComicDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), comicDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(cachedComicDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer|%s/%s (shard|cluster/name) from the cache server", cluster, initialComicDB.Name)
	cachedComicDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialComicDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(cachedComicDBRaw)
}

// testUIDGenerationCreationTime checks if overwriting UID, Generation, CreationTime when the shard annotation is set works
func testUIDGenerationCreationTime(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialMangoDB := newFakeAPIExport("mangodb")
	initialMangoDB.UID = "3"
	initialMangoDB.Generation = 11
	initialMangoDB.CreationTimestamp = metav1.Now().Rfc3339Copy()
	initialMangoDB.Annotations = map[string]string{genericrequest.AnnotationKey: "amber"}
	validateFn := func(mangoDB fakeAPIExport, cachedMangoDBRaw *unstructured.Unstructured) {
		cachedMangoDBJson, err := cachedMangoDBRaw.MarshalJSON()
		require.NoError(t, err)
		cachedMangoDB := &fakeAPIExport{}
		require.NoError(t, json.Unmarshal(cachedMangoDBJson, cachedMangoDB))

		mangoDB.ResourceVersion = cachedMangoDB.ResourceVersion
		mangoDB.Annotations["kcp.dev/cluster"] = cluster.String()
		if !cmp.Equal(cachedMangoDB, &mangoDB) {
			t.Fatalf("received object from the cache server differs from the expected one:\n%s", cmp.Diff(cachedMangoDB, &mangoDB))
		}
	}

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialMangoDB.Name)
	mangoDBRaw, err := toUnstructured(&initialMangoDB)
	require.NoError(t, err)
	cachedMangoDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), mangoDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer|%s/%s (shard|cluster/name) from the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialMangoDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)
}

// testUIDGenerationCreationTimeNegative checks if UID, Generation, CreationTime are set when the shard annotation is NOT set
func testUIDGenerationCreationTimeNegative(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialMangoDB := newFakeAPIExport("mangodbnegative")
	initialMangoDB.UID = "3"
	initialMangoDB.Generation = 8
	initialMangoDB.CreationTimestamp = metav1.Now()
	mangoDBRaw, err := toUnstructured(&initialMangoDB)
	require.NoError(t, err)
	validateFn := func(mangoDB fakeAPIExport, cachedMangoDBRaw *unstructured.Unstructured) {
		cachedMangoDBJson, err := cachedMangoDBRaw.MarshalJSON()
		require.NoError(t, err)
		cachedMangoDB := &fakeAPIExport{}
		require.NoError(t, json.Unmarshal(cachedMangoDBJson, cachedMangoDB))

		if cachedMangoDB.UID == mangoDB.UID {
			t.Fatalf("unexpected UID %v set on amber|%s/%s (shard|cluster/name), an UID should be assinged by the server", cachedMangoDB.UID, cluster, mangoDB.Name)
		}
		if cachedMangoDB.Generation == mangoDB.Generation {
			t.Fatalf("unexpected Generation %v set on amber|%s/%s (shard|cluster/name), a Generation should be assinged by the server", cachedMangoDB.Generation, cluster, mangoDB.Name)
		}
		if cachedMangoDB.CreationTimestamp == mangoDB.CreationTimestamp {
			t.Fatalf("unexpected CreationTimestamp %v set on amber|%s/%s (shard|cluster/name), a CreationTimestamp should be assinged by the server", cachedMangoDB.CreationTimestamp, cluster, mangoDB.Name)
		}

		mangoDB.UID = cachedMangoDB.UID
		mangoDB.Generation = cachedMangoDB.Generation
		mangoDB.ResourceVersion = cachedMangoDB.ResourceVersion
		mangoDB.CreationTimestamp = cachedMangoDB.CreationTimestamp
		mangoDB.Annotations["kcp.dev/cluster"] = cluster.String()
		mangoDB.Annotations["kcp.dev/shard"] = "amber"
		if !cmp.Equal(cachedMangoDB, &mangoDB) {
			t.Fatalf("received object from the cache server differs from the expected one:\n%s", cmp.Diff(cachedMangoDB, &mangoDB))
		}
	}

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), mangoDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer|%s/%s (shard|cluster/name) from the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialMangoDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)
}

// testGenerationOnSpecChanges checks if Generation is not increased when the spec is changed
func testGenerationOnSpecChanges(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialCinnamonDB := newFakeAPIExport("cinnamondb")

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialCinnamonDB.Name)
	cinnamonDBRaw, err := toUnstructured(&initialCinnamonDB)
	require.NoError(t, err)
	cachedCinnamonDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), cinnamonDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err := cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB := &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))

	t.Logf("Update amber|%s/%s (shard|cluster/name) on the cache server", cluster, initialCinnamonDB.Name)
	generationBeforeUpdate := cachedCinnamonDB.Generation
	cachedCinnamonDB.Spec.Size = 5
	cachedCinnamonDBRaw, err = toUnstructured(cachedCinnamonDB)
	require.NoError(t, err)
	cachedCinnamonDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Update(cacheclient.WithShardInContext(ctx, shard.New("amber")), cachedCinnamonDBRaw, metav1.UpdateOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err = cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB = &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))
	if cachedCinnamonDB.Generation != generationBeforeUpdate {
		t.Fatalf("generation musn't be updated after a spec update, generationBeforeUpdate %v, generateAfterUpdate %v, object amber|%s/%s (shard|cluster/name)", generationBeforeUpdate, cachedCinnamonDB.Generation, cluster, initialCinnamonDB.Name)
	}

	// do additional sanity check with GET
	t.Logf("Get abmer|%s/%s (shard|cluster/name) from the cache server", cluster, initialCinnamonDB.Name)
	cachedCinnamonDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialCinnamonDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err = cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB = &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))
	if cachedCinnamonDB.Generation != generationBeforeUpdate {
		t.Fatalf("generation musn't be updated after a spec update, generationBeforeUpdate %v, currentGeneration %v, object amber|%s/%s (shard|cluster/name)", generationBeforeUpdate, cachedCinnamonDB.Generation, cluster, initialCinnamonDB.Name)
	}
}

// testDeletionWithFinalizers checks if deleting an object with finalizers immediately removes it
func testDeletionWithFinalizers(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialGhostDB := newFakeAPIExport("ghostdb")
	initialGhostDB.Finalizers = append(initialGhostDB.Finalizers, "doNotRemove")

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialGhostDB.Name)
	ghostDBRaw, err := toUnstructured(&initialGhostDB)
	require.NoError(t, err)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), ghostDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Remove amber|%s/%s (shard|cluster/name) from the cache server", cluster, initialGhostDB.Name)
	err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Delete(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialGhostDB.Name, metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("Get abmer/%s/%s from the cache server", cluster, initialGhostDB.Name)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(cacheclient.WithShardInContext(ctx, shard.New("amber")), initialGhostDB.Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected to get a NotFound error, got %v", err)
	}
}

// testSpecStatusSimultaneously checks if updating spec and status at the same time works
func testSpecStatusSimultaneously(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)
	initialCucumberDB := newFakeAPIExport("cucumberdb")

	t.Logf("Create abmer|%s/%s (shard|cluster/name) on the cache server", cluster, initialCucumberDB.Name)
	cucumberDBRaw, err := toUnstructured(&initialCucumberDB)
	require.NoError(t, err)
	cachedCucumberDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(cacheclient.WithShardInContext(ctx, shard.New("amber")), cucumberDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	cachedCucumberDBJson, err := cachedCucumberDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCucumberDB := &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCucumberDBJson, cachedCucumberDB))

	t.Logf("Update amber|%s/%s (shard|cluster/name) on the cache server", cluster, initialCucumberDB.Name)
	cachedCucumberDB.Status.Condition = "run out"
	cachedCucumberDB.Spec.Size = 1111
	cachedCucumberDBRaw, err = toUnstructured(cachedCucumberDB)
	require.NoError(t, err)
	cachedCucumberDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Update(cacheclient.WithShardInContext(ctx, shard.New("amber")), cachedCucumberDBRaw, metav1.UpdateOptions{})
	require.NoError(t, err)
	cachedCucumberDBJson, err = cachedCucumberDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCucumberDB = &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCucumberDBJson, cachedCucumberDB))
	if cachedCucumberDB.Spec.Size != 1111 {
		t.Fatalf("unexpected spec.size %v after an update of amber|%s/%s (shard|cluster/name), epxected %v", cachedCucumberDB.Spec.Size, cluster, initialCucumberDB.Name, 1111)
	}
	if cachedCucumberDB.Status.Condition != "run out" {
		t.Fatalf("unexpected status.condition %v after an update of amber|%s/%s (shard|cluster/name), epxected %v", cachedCucumberDB.Status.Condition, cluster, initialCucumberDB.Name, "run out")
	}

}

func newFakeAPIExport(name string) fakeAPIExport {
	return fakeAPIExport{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apis.kcp.dev/v1alpha1",
			Kind:       "APIExport",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{},
		},
	}
}

type fakeAPIExport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              spec   `json:"spec,omitempty"`
	Status            status `json:"status,omitempty"`
}

type spec struct {
	Size int `json:"size,omitempty"`
}

type status struct {
	Condition string `json:"condition,omitempty"`
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	return unstructured, nil
}

func TestCacheServerAllScenarios(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	_, dataDir, err := framework.ScratchDirs(t)
	require.NoError(t, err)
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cacheKubeconfigPath := cache2e.StartStandaloneCacheServer(ctx, t, dataDir)
	cacheServerKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
	require.NoError(t, err)
	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(*cacheServerKubeConfig, "cache", nil, nil)
	cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)

	cacheClientRT := cache2e.CacheClientRoundTrippersFor(cacheClientRestConfig)
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, cacheClientRT, logicalcluster.New("acme"), schema.GroupVersionResource{Group: "apis.kcp.dev", Version: "v1alpha1", Resource: "apiexports"})
		})
	}
}

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Path, gvr schema.GroupVersionResource)
}

// scenarios holds all test scenarios
var scenarios = []testScenario{
	{"TestSchemaIsNotEnforced", testSchemaIsNotEnforced},
	{"TestShardClusterNamesAssigned", testShardClusterNamesAssigned},
	{"TestUIDGenerationCreationTimeOverwrite", testUIDGenerationCreationTime},
	{"TestUIDGenerationCreationTimeNegativeOverwriteNegative", testUIDGenerationCreationTimeNegative},
	{"TestGenerationOnSpecChanges", testGenerationOnSpecChanges},
	{"TestDeletionWithFinalizers", testDeletionWithFinalizers},
	{"TestUpdatingSpecStatusSimultaneously", testSpecStatusSimultaneously},
}
