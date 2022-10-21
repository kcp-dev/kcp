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
	"reflect"
	"strconv"
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
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/dynamic"
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
	{"TestUIDGenerationCreationTimeOverwrite", testUIDGenerationCreationTime},
	{"TestUIDGenerationCreationTimeNegativeOverwriteNegative", testUIDGenerationCreationTimeNegative},
	// TODO: changing spec doesn't increase the Generation of a replicated object
	// TODO: deleting an object with finalizers immediately removes the obj
	// TODO: a shard name is assigned to a replicated obj
	// TODO: schema is not enforced
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
		if !reflect.DeepEqual(cachedMangoDB, &mangoDB) {
			t.Errorf("received object from the cache server differs from the expected one :\n%s", cmp.Diff(cachedMangoDB, &mangoDB))
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

func toUnstructured(obj *fakeAPIExport) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	return unstructured, nil
}

// TODO: refactor with TestAllScenariosAgainstStandaloneCacheServer
func TestAllScenarios(t *testing.T) {
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
		// TODO (p0lyn0mial): check readiness of the cache server
		require.NoError(t, preparedCachedServer.Run(ctx))
	}()
	t.Logf("Creating kubeconfig for the cache server at %s", dataDir)
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
	cacheClientRT := cacheClientRoundTrippersFor(cacheClientRestConfig)

	// TODO (p0lyn0mial): check readiness of the cache server
	time.Sleep(3 * time.Second)

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
