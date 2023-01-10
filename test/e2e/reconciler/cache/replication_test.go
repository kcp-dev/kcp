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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/clientcmd"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface)
}

// scenarios all test scenarios that will be run against in-process and standalone cache server.
var scenarios = []testScenario{
	{"TestReplicateAPIExport", replicateAPIExportScenario},
	{"TestReplicateAPIExportNegative", replicateAPIExportNegativeScenario},
	{"TestReplicateAPIResourceSchema", replicateAPIResourceSchemaScenario},
	{"TestReplicateAPIResourceSchemaNegative", replicateAPIResourceSchemaNegativeScenario},
	{"TestReplicateShard", replicateShardScenario},
	{"TestReplicateShardNegative", replicateShardNegativeScenario},
}

// replicateAPIResourceSchemaScenario tests if an APIResourceSchema is propagated to the cache server.
// The test exercises creation, modification and removal of the APIResourceSchema object.
func replicateAPIResourceSchemaScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	org := framework.NewOrganizationFixture(t, server)
	clusterName := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "today.sheriffs.wild.wild.west"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "APIResourceSchema", gvr: apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"), cluster: clusterName, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source APIResourceSchema %s/%s on the root shard for replication", clusterName, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("today.sheriffs.%s", "wild.wild.west"),
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "wild.wild.west",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "sheriffs",
					Singular: "sheriff",
					Kind:     "Sheriff",
					ListKind: "SheriffList",
				},
				Scope: "Namespaced",
				Versions: []apisv1alpha1.APIResourceVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: runtime.RawExtension{
							Raw: func() []byte {
								ret, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
									Type:        "object",
									Description: "the best sheriff out there",
								})
								if err != nil {
									panic(err)
								}
								return ret
							}(),
						},
					},
				},
			},
		}
	})
	t.Logf("Verify that the source APIResourceSchema %s/%s was replicated to the cache server", clusterName, resourceName)
	scenario.VerifyReplication(ctx, t)

	// note that since the spec of an APIResourceSchema is immutable we are limited to changing some metadata
	t.Logf("Change some metadata on source APIResourceSchema %s/%s and verify if updates were propagated to the cached object", clusterName, resourceName)
	scenario.UpdateMetaSourceResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Verify that deleting source APIResourceSchema %s/%s leads to removal of the cached object", clusterName, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t)
}

// replicateAPIResourceSchemaNegativeScenario checks if modified or even deleted cached APIResourceSchema will be reconciled to match the original object.
func replicateAPIResourceSchemaNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "juicy.mangodbs.db.io"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "APIResourceSchema", gvr: apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"), cluster: cluster, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source APIResourceSchema %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceName,
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "db.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "mangodbs",
					Singular: "mangodb",
					Kind:     "MangoDB",
					ListKind: "MangoDBList",
				},
				Scope: "Namespaced",
				Versions: []apisv1alpha1.APIResourceVersion{
					{
						Name:    "v1",
						Served:  true,
						Storage: true,
						Schema: runtime.RawExtension{
							Raw: func() []byte {
								ret, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
									Type:        "object",
									Description: "the best db out there",
								})
								if err != nil {
									panic(err)
								}

								return ret
							}(),
						},
					},
				},
			},
		}
	})
	t.Logf("Verify that the source APIResourceSchema %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Delete cached APIResourceSchema %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Update cached APIResourceSchema %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateMetaCachedResource(ctx, t)
	t.Logf("Verify that the cached APIResourceSchema %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)
}

// replicateAPIExportScenario tests if an APIExport is propagated to the cache server.
// The test exercises creation, modification and removal of the APIExport object.
func replicateAPIExportScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "wild.wild.west"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "APIExport", gvr: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"), cluster: cluster, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source APIExport %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: "wild.wild.west",
			},
		}
	})
	t.Logf("Verify that the source APIExport %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Change the spec on source APIExport %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResourceWithCustomModifier(ctx, t, func(resUnstructured *unstructured.Unstructured) error {
		apiExport := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(resUnstructured.Object, apiExport); err != nil {
			return err
		}
		apiExport.Spec.LatestResourceSchemas = append(apiExport.Spec.LatestResourceSchemas, "foo.bar")
		return nil
	})
	scenario.VerifyReplication(ctx, t)

	t.Logf("Change some metadata on source APIExport %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateMetaSourceResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Verify that deleting source APIExport %s/%s leads to removal of the cached object", cluster, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t)
}

// replicateAPIExportNegativeScenario checks if modified or even deleted cached APIExport will be reconciled to match the original object.
func replicateAPIExportNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "mangodb"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "APIExport", gvr: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"), cluster: cluster, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source APIExport %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceName,
			},
		}
	})
	t.Logf("Verify that the source APIExport %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Delete cached APIExport %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Update cached APIExport %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateCachedResourceWithCustomModifier(ctx, t, func(res *unstructured.Unstructured) error {
		cachedExport := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(res.Object, cachedExport); err != nil {
			return err
		}
		cachedExport.Spec.LatestResourceSchemas = append(cachedExport.Spec.LatestResourceSchemas, "foo")
		return nil
	})
	t.Logf("Verify that the cached APIExport %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)
}

// replicateShardScenario tests if a Shard is propagated to the cache server.
// The test exercises creation, modification and removal of the Shard object.
func replicateShardScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	// The Shard API is per default only available in the root workspace
	clusterName := core.RootCluster
	resourceName := "test-shard"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "Shard", gvr: corev1alpha1.SchemeGroupVersion.WithResource("shards"), cluster: clusterName, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source Shard %s/%s on the root shard for replication", clusterName, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &corev1alpha1.Shard{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceName,
			},
			Spec: corev1alpha1.ShardSpec{
				BaseURL: "https://base.kcp.test.dev",
			},
		}
	})
	t.Logf("Verify that the source Shard %s/%s was replicated to the cache server", clusterName, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Change the spec on source Shard %s/%s and verify if updates were propagated to the cached object", clusterName, resourceName)
	scenario.UpdateSourceResourceWithCustomModifier(ctx, t, func(res *unstructured.Unstructured) error {
		shard := &corev1alpha1.Shard{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(res.Object, shard); err != nil {
			return err
		}
		shard.Spec.BaseURL = "https://kcp.test.dev"
		return nil
	})
	scenario.VerifyReplication(ctx, t)

	t.Logf("Change some metadata on source Shard %s/%s and verify if updates were propagated to the cached object", clusterName, resourceName)
	scenario.UpdateMetaSourceResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Verify that deleting source Shard %s/%s leads to removal of the cached object", clusterName, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t)
}

// replicateShardNegativeScenario checks if modified or even deleted cached Shard will be reconciled to match the original object.
func replicateShardNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterDynamicClient kcpdynamic.ClusterInterface, cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface) {
	t.Helper()

	// The Shard API is per default only available in the root workspace
	cluster := core.RootCluster
	resourceName := "test-shard-2"
	scenario := &replicateResourceScenario{resourceName: resourceName, kind: "Shard", gvr: corev1alpha1.SchemeGroupVersion.WithResource("shards"), cluster: cluster, server: server, kcpShardClusterDynamicClient: kcpShardClusterDynamicClient, cacheKcpClusterDynamicClient: cacheKcpClusterDynamicClient}

	t.Logf("Create source Shard %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(ctx, t, func() runtime.Object {
		return &corev1alpha1.Shard{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceName,
			},
			Spec: corev1alpha1.ShardSpec{
				BaseURL: "https://base.kcp.test.dev",
			},
		}
	})
	t.Logf("Verify that the source Shard %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Delete cached Shard %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t)
	scenario.VerifyReplication(ctx, t)

	t.Logf("Update cached Shard %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateCachedResourceWithCustomModifier(ctx, t, func(res *unstructured.Unstructured) error {
		shard := &corev1alpha1.Shard{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(res.Object, shard); err != nil {
			return err
		}
		shard.Spec.BaseURL = "https://base2.kcp.test.dev"
		return nil
	})
	t.Logf("Verify that the cached Shard %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t)
}

// TestCacheServerInProcess runs all test scenarios against a cache server that runs with a kcp server.
func TestCacheServerInProcess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)...))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardDynamicClient, err := kcpdynamic.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	cacheClientRT := ClientRoundTrippersFor(kcpRootShardConfig)
	cacheKcpClusterDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, server, kcpRootShardDynamicClient, cacheKcpClusterDynamicClient)
		})
	}
}

// TestCacheServerStandalone runs all test scenarios against a standalone cache server.
func TestCacheServerStandalone(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	artifactDir, dataDir, err := framework.ScratchDirs(t)
	require.NoError(t, err)
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cacheKubeconfigPath := StartStandaloneCacheServer(ctx, t, dataDir)

	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile), fmt.Sprintf("--cache-server-kubeconfig-file=%s", cacheKubeconfigPath))...),
		framework.WithScratchDirectories(artifactDir, dataDir),
	)
	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardDynamicClient, err := kcpdynamic.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)

	cacheServerKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
	require.NoError(t, err)
	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(*cacheServerKubeConfig, "cache", nil, nil)
	cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)
	cacheClientRT := ClientRoundTrippersFor(cacheClientRestConfig)
	cacheKcpClusterDynamicClient, err := kcpdynamic.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, server, kcpRootShardDynamicClient, cacheKcpClusterDynamicClient)
		})
	}
}

// replicateResourceScenario an auxiliary struct that is used by all test scenarios defined in this pkg.
type replicateResourceScenario struct {
	resourceName string
	cluster      logicalcluster.Name

	gvr  schema.GroupVersionResource
	kind string

	server                       framework.RunningServer
	kcpShardClusterDynamicClient kcpdynamic.ClusterInterface
	cacheKcpClusterDynamicClient kcpdynamic.ClusterInterface
}

func (b *replicateResourceScenario) CreateSourceResource(ctx context.Context, t *testing.T, getSourceResource func() runtime.Object) {
	t.Helper()
	resUnstructured, err := toUnstructured(getSourceResource(), b.kind, b.gvr)
	require.NoError(t, err)
	_, err = b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Create(ctx, resUnstructured, metav1.CreateOptions{})
	require.NoError(t, err)
}

func (b *replicateResourceScenario) UpdateMetaSourceResource(ctx context.Context, t *testing.T) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := b.changeMetadataFor(res); err != nil {
			return err
		}
		resUnstructured, err := toUnstructured(res, b.kind, b.gvr)
		if err != nil {
			return err
		}
		_, err = b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(ctx, resUnstructured, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateSourceResourceWithCustomModifier(ctx context.Context, t *testing.T, modFunc func(*unstructured.Unstructured) error) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := modFunc(res); err != nil {
			return err
		}
		_, err := b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(ctx, res, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateMetaCachedResource(ctx context.Context, t *testing.T) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := b.changeMetadataFor(res); err != nil {
			return err
		}
		resUnstructured, err := toUnstructured(res, b.kind, b.gvr)
		if err != nil {
			return err
		}
		_, err = b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(cacheclient.WithShardInContext(ctx, shard.New("root")), resUnstructured, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) UpdateCachedResourceWithCustomModifier(ctx context.Context, t *testing.T, modFunc func(*unstructured.Unstructured) error) {
	t.Helper()
	b.resourceUpdateHelper(ctx, t, func(ctx context.Context) (*unstructured.Unstructured, error) {
		return b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	}, func(res *unstructured.Unstructured) error {
		if err := modFunc(res); err != nil {
			return err
		}
		_, err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Update(cacheclient.WithShardInContext(ctx, shard.New("root")), res, metav1.UpdateOptions{})
		return err
	})
}

func (b *replicateResourceScenario) DeleteSourceResourceAndVerify(ctx context.Context, t *testing.T) {
	t.Helper()
	require.NoError(t, b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Delete(ctx, b.resourceName, metav1.DeleteOptions{}))
	framework.Eventually(t, func() (bool, string) {
		_, err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("replicated %s %s/%s wasn't removed", b.gvr, b.cluster, b.resourceName)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) DeleteCachedResource(ctx context.Context, t *testing.T) {
	t.Helper()
	err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.DeleteOptions{})
	require.NoError(t, err)
}

func (b *replicateResourceScenario) VerifyReplication(ctx context.Context, t *testing.T) {
	t.Helper()
	b.verifyResourceReplicationHelper(ctx, t)
}

func (b *replicateResourceScenario) changeMetadataFor(originalResource *unstructured.Unstructured) error {
	originalResourceMeta, err := meta.Accessor(originalResource)
	if err != nil {
		return err
	}
	annotations := originalResourceMeta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["testAnnotation"] = "testAnnotationValue"
	originalResourceMeta.SetAnnotations(annotations)
	return nil
}

func (b *replicateResourceScenario) resourceUpdateHelper(ctx context.Context, t *testing.T, resourceGetter func(ctx context.Context) (*unstructured.Unstructured, error), resourceUpdater func(*unstructured.Unstructured) error) {
	t.Helper()
	framework.Eventually(t, func() (bool, string) {
		resource, err := resourceGetter(ctx)
		if err != nil {
			return false, err.Error()
		}
		err = resourceUpdater(resource)
		if err != nil {
			if !errors.IsConflict(err) {
				return false, fmt.Sprintf("unknown error while updating the cached %s/%s/%s, err: %s", b.gvr, b.cluster, b.resourceName, err.Error())
			}
			return false, err.Error() // try again
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) verifyResourceReplicationHelper(ctx context.Context, t *testing.T) {
	t.Helper()
	cluster := b.cluster.Path()
	t.Logf("Get %s %s/%s from the root shard and the cache server for comparison", b.gvr, cluster, b.resourceName)
	framework.Eventually(t, func() (bool, string) {
		originalResource, err := b.kcpShardClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(ctx, b.resourceName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cachedResource, err := b.cacheKcpClusterDynamicClient.Resource(b.gvr).Cluster(b.cluster.Path()).Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
		if err != nil {
			if !errors.IsNotFound(err) {
				return true, err.Error()
			}
			return false, err.Error()
		}
		t.Logf("Compare if both the original and replicated resources (%s %s/%s) are the same except %s annotation and ResourceVersion", b.gvr, cluster, b.resourceName, genericapirequest.AnnotationKey)
		cachedResourceMeta, err := meta.Accessor(cachedResource)
		if err != nil {
			return false, err.Error()
		}
		if _, found := cachedResourceMeta.GetAnnotations()[genericapirequest.AnnotationKey]; !found {
			t.Fatalf("replicated %s root|%s/%s, doesn't have %s annotation", b.gvr, cluster, cachedResourceMeta.GetName(), genericapirequest.AnnotationKey)
		}
		unstructured.RemoveNestedField(originalResource.Object, "metadata", "resourceVersion")
		unstructured.RemoveNestedField(cachedResource.Object, "metadata", "resourceVersion")
		unstructured.RemoveNestedField(cachedResource.Object, "metadata", "annotations", genericapirequest.AnnotationKey)
		if cachedStatus, ok := cachedResource.Object["status"]; ok && cachedStatus == nil || (cachedStatus != nil && len(cachedStatus.(map[string]interface{})) == 0) {
			// TODO: worth investigating:
			// for some reason cached resources have an empty status set whereas the original resources don't
			unstructured.RemoveNestedField(cachedResource.Object, "status")
		}
		if diff := cmp.Diff(cachedResource.Object, originalResource.Object); len(diff) > 0 {
			return false, fmt.Sprintf("replicated %s root|%s/%s is different from the original", b.gvr, cluster, cachedResourceMeta.GetName())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func toUnstructured(obj interface{}, kind string, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	unstructured.SetKind(kind)
	unstructured.SetAPIVersion(gvr.GroupVersion().String())
	return unstructured, nil
}
