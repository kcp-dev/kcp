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

package cache

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	corev1 "k8s.io/api/core/v1"
	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	cache2e "github.com/kcp-dev/kcp/test/e2e/reconciler/cache"
)

//go:embed assets/*.yaml
var testFiles embed.FS

func TestCacheServerReplicationAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	testCases := make([]struct {
		name string
	}, 10)
	for i := range testCases {
		testCases[i].name = fmt.Sprintf("test-case-%d", i)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := server.BaseConfig(t)
			cacheClientRT := cache2e.ClientRoundTrippersFor(cfg)
			cacheClientRT.UserAgent = "kcp-cache-test"
			rootOrg, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

			orgProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "error creating cowboys provider kcp client")

			dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
			require.NoError(t, err, "error creating dynamic cluster client")

			extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
			require.NoError(t, err, "error creating apiextensions cluster client")

			require.NoError(t, err, "error creating kcp cache client")

			t.Logf("Install a instances object CRD into workspace %q", rootOrg)
			mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(rootOrg).Discovery()))

			err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/crd-instances.yaml", testFiles)
			require.NoError(t, err)

			t.Logf("Waiting for CRD to be established")
			name := "instances.machines.svm.io"
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				crd, err := extensionsClusterClient.Cluster(rootOrg).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
				require.NoError(t, err)
				return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

			mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(rootOrg).Discovery()))

			t.Logf("Create a instances object in workspace %q", rootOrg)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				err := helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/instances.yaml", testFiles)
				if err != nil {
					mapper.Reset()
					return false, err.Error()
				}
				return true, ""
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for instances object to be created")

			t.Logf("Create a publishedResource in workspace %q", rootOrg)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				err := helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/published-resource-instances.yaml", testFiles)
				if err != nil {
					mapper.Reset()
					return false, err.Error()
				}
				return true, ""
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for publishedResource to be created")

			t.Logf("Wait for replication api to be ready and reporting 7 replicas")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				publishedResource, err := orgProviderKCPClient.Cluster(rootOrg).CacheV1alpha1().CachedResources().Get(ctx, "instances", metav1.GetOptions{})
				require.NoError(t, err)
				return publishedResource != nil && publishedResource.Status.ResourceCounts != nil && publishedResource.Status.ResourceCounts.Cache == 7, yamlMarshal(t, publishedResource)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for replication api to be ready and reporting 7 replicas")

			t.Logf("Delete some resources and wait for the cache to be updated")
			err = dynamicClusterClient.Cluster(rootOrg).Resource(schema.GroupVersionResource{Group: "machines.svm.io", Version: "v1alpha1", Resource: "instances"}).Delete(ctx, "analytics-1", metav1.DeleteOptions{})
			require.NoError(t, err)
			err = dynamicClusterClient.Cluster(rootOrg).Resource(schema.GroupVersionResource{Group: "machines.svm.io", Version: "v1alpha1", Resource: "instances"}).Delete(ctx, "analytics-2", metav1.DeleteOptions{})
			require.NoError(t, err)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				publishedResource, err := orgProviderKCPClient.Cluster(rootOrg).CacheV1alpha1().CachedResources().Get(ctx, "instances", metav1.GetOptions{})
				require.NoError(t, err)
				return publishedResource != nil && publishedResource.Status.ResourceCounts != nil && publishedResource.Status.ResourceCounts.Cache == 5, yamlMarshal(t, publishedResource)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for replication api to be ready and reporting 5 replicas")
		})
	}
}

func TestReplicationWithWildcardListing(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// Set up clients.

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")
	_ = dynamicClusterClient

	extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	cacheClientCfg := createCacheClientConfigForEnvironment(ctx, t, server.RootShardSystemMasterBaseConfig(t))
	cacheClientRT := cache2e.ClientRoundTrippersFor(cacheClientCfg)
	cacheDynClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err, "failed to construct cache dynamic client")
	_ = cacheDynClient

	// Set up workspaces.

	orgPath, orgWS := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	wsA1Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	wsA2Path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	wsB1Path, wsB1 := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// Set up artifacts in these workspaces.

	identityA := "identity-a-" + string(orgWS.UID)
	identityB := "identity-b-" + string(orgWS.UID)
	// ^^ Appending random string (uid) to identities so that if we have stale replicas
	// from previous failed runs, they don't affect future runs and fail the List() checks.
	var identityHashesM sync.Mutex
	identityHashes := make(map[string]string) // Identity -> Hash

	t.Run("Prepare workspaces", func(t *testing.T) {
		for ws, identity := range map[logicalcluster.Path]string{
			wsA1Path: identityA, wsA2Path: identityA, // A's...
			wsB1Path: identityB, // B's...
		} {
			t.Run(fmt.Sprintf("Prepare workspace %q", ws), func(t *testing.T) {
				t.Parallel()
				var err error

				// We need the Instances CRD + a few of its CRs for testing and a CachedResource for that Instance resource + its identity secret.
				// We iterate over two different identities, so we should have two distinct Instance resources across the cache server.

				t.Logf("Install Instances CRD into workspace %q", ws)
				mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(ws).Discovery()))

				err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(ws), mapper, nil, "assets/crd-instances.yaml", testFiles)
				require.NoError(t, err)

				t.Logf("Waiting for CRD to be established")
				name := "instances.machines.svm.io"
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					crd, err := extensionsClusterClient.Cluster(ws).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
					require.NoError(t, err)
					return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

				mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(ws).Discovery()))

				t.Logf("Create Instance objects in workspace %q", ws)
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					err := helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(ws), mapper, nil, "assets/instances.yaml", testFiles)
					return err == nil, ""
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for instances object to be created")

				t.Logf("Creating identity Secret in workspace %q", ws)
				_, err = kubeClusterClient.Cluster(ws).CoreV1().Secrets("default").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name: "identity",
					},
					Type: corev1.SecretTypeOpaque,
					StringData: map[string]string{
						"key": identity,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "error creating identity secret %s workspace", ws)

				t.Logf("Create a CachedResource in workspace %q", ws)
				_, err = kcpClusterClient.Cluster(ws).CacheV1alpha1().CachedResources().
					Create(ctx, &cachev1alpha1.CachedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name: "instances",
						},
						Spec: cachev1alpha1.CachedResourceSpec{
							GroupVersionResource: cachev1alpha1.GroupVersionResource{
								Group:    "machines.svm.io",
								Version:  "v1alpha1",
								Resource: "instances",
							},
							Identity: &cachev1alpha1.Identity{
								SecretRef: &corev1.SecretReference{
									Name:      "identity",
									Namespace: "default",
								},
							},
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app.kubernetes.io/part-of": "instances",
								},
							},
						},
					}, metav1.CreateOptions{})
				require.NoError(t, err, "error creating CachedResource in workspace %s", ws)

				t.Logf("Wait while the Instance objects in workspace %q are replicated", ws)
				kcptestinghelpers.Eventually(t, func() (bool, string) {
					cr, err := kcpClusterClient.Cluster(ws).CacheV1alpha1().CachedResources().
						Get(ctx, "instances", metav1.GetOptions{})
					require.NoError(t, err)
					if cr.Status.ResourceCounts == nil {
						return false, "ResourceCounts should not be nil"
					}
					if cr.Status.ResourceCounts.Local != 7 {
						return false, fmt.Sprintf("Count of local objects should be 8, got %d", cr.Status.ResourceCounts.Local)
					}
					if cr.Status.ResourceCounts.Cache != 7 {
						return false, fmt.Sprintf("Count of cached objects should be 8, got %d", cr.Status.ResourceCounts.Cache)
					}
					return true, ""
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

				cr, err := kcpClusterClient.Cluster(ws).CacheV1alpha1().CachedResources().
					Get(ctx, "instances", metav1.GetOptions{})
				require.NoError(t, err)
				require.NotEmpty(t, cr.Status.IdentityHash, "identity hash should not be empty")
				identityHashesM.Lock()
				identityHashes[identity] = cr.Status.IdentityHash
				identityHashesM.Unlock()
			})
		}
	})

	// Wildcard list across A's and B's.

	t.Logf("Make sure wildcard list for %s identity (hash %s) returns 14 objects", identityA, identityHashes[identityA])
	listA, err := cacheDynClient.Resource(schema.GroupVersionResource{
		Group:    "machines.svm.io",
		Version:  "v1alpha1",
		Resource: "instances" + ":" + identityHashes[identityA],
	}).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, listA.Items, 14, "wildcard list of identity-a Instance objects have unexpected number of items")

	t.Logf("Make sure wildcard list for %s identity (hash %s) returns 7 objects", identityB, identityHashes[identityB])
	listB, err := cacheDynClient.Resource(schema.GroupVersionResource{
		Group:    "machines.svm.io",
		Version:  "v1alpha1",
		Resource: "instances" + ":" + identityHashes[identityB],
	}).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, listB.Items, 7, "wildcard list of identity-b Instance objects have unexpected number of items")

	// Scoped get in <B1's shard>:<B1 workspace>, no identity needed.

	shardForB1, err := kcptesting.WorkspaceShard(t.Context(), kcpClusterClient, wsB1)
	b1ShardName := shard.Name(shardForB1.Name)
	b1ClusterName := logicalcluster.NewPath(wsB1.Spec.Cluster)
	require.NoError(t, err, "should be able to get shard for workspace")
	t.Logf("Make sure scoped Instance get from cache with shard=%q and cluster=%q works", b1ShardName, b1ClusterName)
	_, err = cacheDynClient.Cluster(b1ClusterName).Resource(schema.GroupVersionResource{
		Group:    "machines.svm.io",
		Version:  "v1alpha1",
		Resource: "instances",
	}).Get(
		cacheclient.WithShardInContext(t.Context(), b1ShardName),
		"web-server-1",
		metav1.GetOptions{},
	)
	require.NoError(t, err, "scoped get from cache should succeed")

	// Once the CachedResource in B1's ws is deleted, the instances resource should be gone from cache too.

	t.Logf("Wait while CachedResource in %q workspace is deleted", wsB1Path)
	err = kcpClusterClient.CacheV1alpha1().CachedResources().Cluster(wsB1Path).Delete(t.Context(), "instances", metav1.DeleteOptions{})
	require.NoError(t, err, "deleting CachedResource %s|%s should not fail", wsB1Path, "instances")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.CacheV1alpha1().CachedResources().Cluster(wsB1Path).Get(t.Context(), "instances", metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("expected NotFound, got err=%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CachedResource to be deleted")
	// Just to be safe it's better to be polling for NotFound, because the CachedResource informer
	// in cache-server's CRD lister might be behind, and/or the resources still being GC'd.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = cacheDynClient.Cluster(b1ClusterName).Resource(schema.GroupVersionResource{
			Group:    "machines.svm.io",
			Version:  "v1alpha1",
			Resource: "instances",
		}).Get(
			cacheclient.WithShardInContext(t.Context(), b1ShardName),
			"web-server-1",
			metav1.GetOptions{},
		)
		return apierrors.IsNotFound(err), fmt.Sprintf("expected NotFound, got err=%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for Instance resource to be NotFound")
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}

// TODO(gman0): taken from kcp/test/e2e/reconciler/cache/replication_test.go . Consolidate this. Additionally, this doesn't support an external shared cache.
// createCacheClientConfigForEnvironment is a helper function
// for creating a rest config for the cache server depending on
// the underlying test environment.
func createCacheClientConfigForEnvironment(ctx context.Context, t *testing.T, kcpRootShardConfig *rest.Config) *rest.Config {
	// TODO: in the future we might associate a shard instance with a cache server
	// via some field on Shard resources, in that case we could read the value of
	// that field for creating a rest config.
	t.Helper()
	kcpRootShardClient, err := kcpclientset.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	shards, err := kcpRootShardClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	if len(shards.Items) == 1 {
		// Single shard with embedded cache server — use the loopback
		// bearer token which the embedded cache already trusts.
		return kcpRootShardConfig
	}

	// assume multi-shard env created by the sharded-test-server
	cacheServerKubeConfigPath := filepath.Join(filepath.Dir(kcptesting.KubeconfigPath()), "..", ".kcp-cache", "cache.kubeconfig")
	cacheServerKubeConfig, err := kcptestinghelpers.LoadKubeConfig(cacheServerKubeConfigPath, "cache")
	require.NoError(t, err)
	cacheServerRestConfig, err := cacheServerKubeConfig.ClientConfig()
	require.NoError(t, err)
	return cacheServerRestConfig
}
