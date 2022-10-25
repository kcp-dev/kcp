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
	"fmt"
	"path"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apimachineryerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/dynamic"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	cacheserver "github.com/kcp-dev/kcp/pkg/cache/server"
	cacheopitons "github.com/kcp-dev/kcp/pkg/cache/server/options"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource)
}

// scenarios holds all test scenarios
var scenarios = []testScenario{
	{"TestSchemaIsNotEnforced", testSchemaIsNotEnforced},
	{"TestShardClusterNamesAssigned", testShardClusterNamesAssigned},
	{"TestUIDGenerationCreationTimeOverwrite", testUIDGenerationCreationTime},
	{"TestUIDGenerationCreationTimeNegativeOverwriteNegative", testUIDGenerationCreationTimeNegative},
	{"TestGenerationOnSpecChanges", testGenerationOnSpecChanges},
	// TODO: changing spec doesn't increase the Generation of a replicated object
	// TODO: deleting an object with finalizers immediately removes the obj
	// TODO: spec and status can be updated at the same time
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

// testSchemaIsNotEnforced checks if an object of any schema can be stored as "apis.kcp.dev.v1alpha1.apiexports"
func testSchemaIsNotEnforced(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := dynamic.NewClusterForConfig(cacheClientRT)
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

	t.Logf("Create abmer/%s/earth on the cache server without type information", cluster)
	earthRaw, err := toUnstructured(&earth)
	require.NoError(t, err)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, earthRaw, metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected to receive an error when storing an object without providing TypeMeta")
	}

	earth.APIVersion = "apis.kcp.dev/v1alpha1"
	earth.Kind = "APIExport"
	t.Logf("Create abmer/%s/earth on the cache server without providing a name", cluster)
	earthRaw, err = toUnstructured(&earth)
	require.NoError(t, err)
	_, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, earthRaw, metav1.CreateOptions{})
	if err == nil {
		t.Fatalf("expected to receive an error when storing an object without a name")
	}

	earth.Name = "earth"
	t.Logf("Create abmer/%s/%s on the cache server", cluster, earth.Name)
	earthRaw, err = toUnstructured(&earth)
	require.NoError(t, err)
	cachedEarthRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, earthRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(earth, cachedEarthRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer/%s/%s from the cache server", cluster, earth.Name)
	cachedEarthRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(ctx, earth.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(earth, cachedEarthRaw)
}

// testShardNamesAssigned checks if a shard name is provided in the "kcp.dev/shard" annotation and
// if a cluster name is stored at "kcp.dev/cluster" annotation
func testShardClusterNamesAssigned(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := dynamic.NewClusterForConfig(cacheClientRT)
	require.NoError(t, err)
	initialComicDB := newFakeAPIExport("comicdb")
	validateFn := func(cachedComicDBRaw *unstructured.Unstructured) {
		cachedComicDBJson, err := cachedComicDBRaw.MarshalJSON()
		require.NoError(t, err)
		cachedComicDB := &fakeAPIExport{}
		require.NoError(t, json.Unmarshal(cachedComicDBJson, cachedComicDB))
		if cachedComicDB.Annotations["kcp.dev/shard"] != "amber" {
			t.Fatalf("unexpected shard name %v assigned to cached amber/%s/%s, expected %s", cachedComicDB.Annotations["kcp.dev/shard"], cluster, cachedComicDB.Name, "amber")
		}
		if cachedComicDB.Annotations["kcp.dev/cluster"] != cluster.String() {
			t.Fatalf("unexpected cluster name %v assigned to cached amber/%s/%s, expected %s", cachedComicDB.Annotations["kcp.dev/cluster"], cluster, cachedComicDB.Name, cluster.String())
		}
	}

	t.Logf("Create abmer/%s/%s on the cache server", cluster, initialComicDB.Name)
	comicDBRaw, err := toUnstructured(&initialComicDB)
	require.NoError(t, err)
	cachedComicDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, comicDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(cachedComicDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer/%s/%s from the cache server", cluster, initialComicDB.Name)
	cachedComicDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(ctx, initialComicDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(cachedComicDBRaw)
}

// testUIDGenerationCreationTime checks if overwriting UID, Generation, CreationTime when the shard annotation is set works
func testUIDGenerationCreationTime(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := dynamic.NewClusterForConfig(cacheClientRT)
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

	t.Logf("Create abmer/%s/%s on the cache server", cluster, initialMangoDB.Name)
	mangoDBRaw, err := toUnstructured(&initialMangoDB)
	require.NoError(t, err)
	cachedMangoDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, mangoDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer/%s/%s from the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(ctx, initialMangoDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)
}

// testUIDGenerationCreationTimeNegative checks if UID, Generation, CreationTime are set when the shard annotation is NOT set
func testUIDGenerationCreationTimeNegative(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := dynamic.NewClusterForConfig(cacheClientRT)
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
			t.Fatalf("unexpected UID %v set on amber/%s/%s, an UID should be assinged by the server", cachedMangoDB.UID, cluster, mangoDB.Name)
		}
		if cachedMangoDB.Generation == mangoDB.Generation {
			t.Fatalf("unexpected Generation %v set on amber/%s/%s, a Generation should be assinged by the server", cachedMangoDB.Generation, cluster, mangoDB.Name)
		}
		if cachedMangoDB.CreationTimestamp == mangoDB.CreationTimestamp {
			t.Fatalf("unexpected CreationTimestamp %v set on amber/%s/%s, a CreationTimestamp should be assinged by the server", cachedMangoDB.CreationTimestamp, cluster, mangoDB.Name)
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

	t.Logf("Create abmer/%s/%s on the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, mangoDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)

	// do additional sanity check with GET
	t.Logf("Get abmer/%s/%s from the cache server", cluster, initialMangoDB.Name)
	cachedMangoDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(ctx, initialMangoDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	validateFn(initialMangoDB, cachedMangoDBRaw)
}

// testGenerationOnSpecChanges checks if Generation is not increased when the spec is changed
func testGenerationOnSpecChanges(ctx context.Context, t *testing.T, cacheClientRT *rest.Config, cluster logicalcluster.Name, gvr schema.GroupVersionResource) {
	cacheDynamicClient, err := dynamic.NewClusterForConfig(cacheClientRT)
	require.NoError(t, err)
	initialCinnamonDB := newFakeAPIExport("cinnamondb")

	t.Logf("Create abmer/%s/%s on the cache server", cluster, initialCinnamonDB.Name)
	cinnamonDBRaw, err := toUnstructured(&initialCinnamonDB)
	require.NoError(t, err)
	cachedCinnamonDBRaw, err := cacheDynamicClient.Cluster(cluster).Resource(gvr).Create(ctx, cinnamonDBRaw, metav1.CreateOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err := cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB := &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))

	t.Logf("Update amber/%s/%s on the cache server", cluster, initialCinnamonDB.Name)
	generationBeforeUpdate := cachedCinnamonDB.Generation
	cachedCinnamonDB.Spec.Size = 5
	cachedCinnamonDBRaw, err = toUnstructured(cachedCinnamonDB)
	require.NoError(t, err)
	cachedCinnamonDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Update(ctx, cachedCinnamonDBRaw, metav1.UpdateOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err = cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB = &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))
	if cachedCinnamonDB.Generation != generationBeforeUpdate {
		t.Fatalf("generation musn't be updated after a spec update, generationBeforeUpdate %v, generateAfterUpdate %v, object amber/%s/%s", generationBeforeUpdate, cachedCinnamonDB.Generation, cluster, initialCinnamonDB.Name)
	}

	// do additional sanity check with GET
	t.Logf("Get abmer/%s/%s from the cache server", cluster, initialCinnamonDB.Name)
	cachedCinnamonDBRaw, err = cacheDynamicClient.Cluster(cluster).Resource(gvr).Get(ctx, initialCinnamonDB.Name, metav1.GetOptions{})
	require.NoError(t, err)
	cachedCinnamonDBJson, err = cachedCinnamonDBRaw.MarshalJSON()
	require.NoError(t, err)
	cachedCinnamonDB = &fakeAPIExport{}
	require.NoError(t, json.Unmarshal(cachedCinnamonDBJson, cachedCinnamonDB))
	if cachedCinnamonDB.Generation != generationBeforeUpdate {
		t.Fatalf("generation musn't be updated after a spec update, generationBeforeUpdate %v, currentGeneration %v, object amber/%s/%s", generationBeforeUpdate, cachedCinnamonDB.Generation, cluster, initialCinnamonDB.Name)
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

func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	return unstructured, nil
}

// TODO: refactor with TestAllScenariosAgainstStandaloneCacheServer
func TestCacheServerAllScenarios(t *testing.T) {
	_, dataDir, err := framework.ScratchDirs(t)
	require.NoError(t, err)

	cacheServerPortStr, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerPort, err := strconv.Atoi(cacheServerPortStr)
	require.NoError(t, err)
	cacheServerOptions := cacheopitons.NewOptions(path.Join(dataDir, "cache"))
	cacheServerOptions.SecureServing.BindPort = cacheServerPort
	cacheServerEmbeddedEtcdClientPort, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerEmbeddedEtcdPeerPort, err := framework.GetFreePort(t)
	require.NoError(t, err)
	cacheServerOptions.EmbeddedEtcd.ClientPort = cacheServerEmbeddedEtcdClientPort
	cacheServerOptions.EmbeddedEtcd.PeerPort = cacheServerEmbeddedEtcdPeerPort
	cacheServerCompletedOptions, err := cacheServerOptions.Complete()
	require.NoError(t, err)
	if errs := cacheServerCompletedOptions.Validate(); len(errs) > 0 {
		require.NoError(t, apimachineryerrors.NewAggregate(errs))
	}
	cacheServerConfig, err := cacheserver.NewConfig(cacheServerCompletedOptions, nil)
	require.NoError(t, err)
	cacheServerCompletedConfig, err := cacheServerConfig.Complete()
	require.NoError(t, err)
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)
	if cacheServerCompletedConfig.EmbeddedEtcd.Config != nil {
		t.Logf("Starting embedded etcd for the cache server")
		require.NoError(t, embeddedetcd.NewServer(cacheServerCompletedConfig.EmbeddedEtcd).Run(ctx))
	}
	cacheServer, err := cacheserver.NewServer(cacheServerCompletedConfig)
	require.NoError(t, err)
	preparedCachedServer, err := cacheServer.PrepareRun(ctx)
	require.NoError(t, err)
	t.Logf("Starting the cache server")
	go func() {
		require.NoError(t, preparedCachedServer.Run(ctx))
	}()
	cacheServerKubeConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cache": {
				Server:               fmt.Sprintf("https://localhost:%s", cacheServerPortStr),
				CertificateAuthority: path.Join(dataDir, "cache", "apiserver.crt"),
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"cache": {
				Cluster: "cache",
			},
		},
		CurrentContext: "cache",
	}

	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(cacheServerKubeConfig, "cache", nil, nil)
	cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)
	t.Logf("waiting for the cache server at %v to become ready", cacheClientRestConfig.Host)
	waitUntilCacheServerIsReady(ctx, t, cacheClientRestConfig)
	cacheClientRT := cacheClientRoundTrippersFor(cacheClientRestConfig)

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			scenario.work(ctx, tt, cacheClientRT, logicalcluster.New("acme"), schema.GroupVersionResource{Group: "apis.kcp.dev", Version: "v1alpha1", Resource: "apiexports"})
		})
	}
}

// TODO: refactor with TestAllScenariosAgainstStandaloneCacheServer
func cacheClientRoundTrippersFor(cfg *rest.Config) *rest.Config {
	cacheClientRT := cacheclient.WithCacheServiceRoundTripper(rest.CopyConfig(cfg))
	cacheClientRT = cacheclient.WithShardNameFromContextRoundTripper(cacheClientRT)
	cacheClientRT = cacheclient.WithDefaultShardRoundTripper(cacheClientRT, "amber")
	return cacheClientRT
}

func waitUntilCacheServerIsReady(ctx context.Context, t *testing.T, cacheClientRT *rest.Config) {
	cacheClientRT = rest.CopyConfig(cacheClientRT)
	if cacheClientRT.NegotiatedSerializer == nil {
		cacheClientRT.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	}
	client, err := rest.UnversionedRESTClientFor(cacheClientRT)
	if err != nil {
		t.Fatalf("failed to create unversioned client: %v", err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/livez", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			waitForEndpoint(ctx, t, client, endpoint)
		}(endpoint)
	}
	wg.Wait()
}

func waitForEndpoint(ctx context.Context, t *testing.T, client *rest.RESTClient, endpoint string) {
	var lastError error
	if err := wait.PollImmediateWithContext(ctx, 100*time.Millisecond, time.Minute, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		_, err := req.Do(ctx).Raw()
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: failed components: %w", req.URL(), err)
			return false, nil
		}

		t.Logf("success contacting %s", req.URL())
		return true, nil
	}); err != nil && lastError != nil {
		t.Error(lastError)
	}
}
