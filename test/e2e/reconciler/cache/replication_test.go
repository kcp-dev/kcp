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
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/clientcmd"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface)
}

// scenarios all test scenarios that will be run against in-process and standalone cache server
var scenarios = []testScenario{
	{"TestReplicateAPIExport", replicateAPIExportScenario},
	{"TestReplicateAPIExportNegative", replicateAPIExportNegativeScenario},
	{"TestReplicateAPIResourceSchema", replicateAPIResourceSchemaScenario},
	{"TestReplicateAPIResourceSchemaNegative", replicateAPIResourceSchemaNegativeScenario},
	{"TestReplicateClusterWorkspaceShard", replicateClusterWorkspaceShardScenario},
	{"TestReplicateClusterWorkspaceShardNegative", replicateClusterWorkspaceShardNegativeScenario},
}

// replicateAPIResourceSchemaScenario tests if an APIResourceSchema is propagated to the cache server.
// The test exercises creation, modification and removal of the APIResourceSchema object.
func replicateAPIResourceSchemaScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "today.sheriffs.wild.wild.west"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "APIResourceSchema", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source APIResourceSchema %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		apifixtures.CreateSheriffsSchemaAndExport(ctx, t, cluster, kcpShardClusterClient, "wild.wild.west", "testing replication to the cache server")
		return nil
	})
	t.Logf("Verify that the source APIResourceSchema %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	// note that since the spec of an APIResourceSchema is immutable we are limited to changing some metadata
	t.Logf("Change some metadata on source APIResourceSchema %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResource(ctx, t, cluster, func(res runtime.Object) error {
		if err := scenario.ChangeMetadataFor(res); err != nil {
			return err
		}
		apiResSchema, ok := res.(*apisv1alpha1.APIResourceSchema)
		if !ok {
			return fmt.Errorf("%T is not *APIResourceSchema", res)
		}
		_, err := kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Update(ctx, apiResSchema, metav1.UpdateOptions{})
		return err
	})
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Verify that deleting source APIResourceSchema %s/%s leads to removal of the cached object", cluster, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t, cluster)
}

// replicateAPIResourceSchemaNegativeScenario checks if modified or even deleted cached APIResourceSchema will be reconciled to match the original object
func replicateAPIResourceSchemaNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "juicy.mangodbs.db.io"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "APIResourceSchema", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source APIResourceSchema %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		schema := &apisv1alpha1.APIResourceSchema{
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
		_, err := kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
		return err
	})
	t.Logf("Verify that the source APIResourceSchema %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Delete cached APIResourceSchema %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t, cluster)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Update cached APIResourceSchema %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateCachedResource(ctx, t, cluster, func(res runtime.Object) error {
		cachedSchema, ok := res.(*apisv1alpha1.APIResourceSchema)
		if !ok {
			return fmt.Errorf("%T is not *APIResourceSchema", res)
		}
		// since the spec of an APIResourceSchema is immutable
		// let's modify some metadata
		if cachedSchema.Labels == nil {
			cachedSchema.Labels = map[string]string{}
		}
		cachedSchema.Labels["foo"] = "bar"
		_, err := cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), cachedSchema, metav1.UpdateOptions{})
		return err
	})
	t.Logf("Verify that the cached APIResourceSchema %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)
}

// replicateAPIExportScenario tests if an APIExport is propagated to the cache server.
// The test exercises creation, modification and removal of the APIExport object.
func replicateAPIExportScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "wild.wild.west"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "APIExport", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source APIExport %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		apifixtures.CreateSheriffsSchemaAndExport(ctx, t, cluster, kcpShardClusterClient, "wild.wild.west", "testing replication to the cache server")
		return nil
	})
	t.Logf("Verify that the source APIExport %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Change the spec on source APIExport %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResource(ctx, t, cluster, func(res runtime.Object) error {
		apiExport, ok := res.(*apisv1alpha1.APIExport)
		if !ok {
			return fmt.Errorf("%T is not *APIExport", res)
		}
		apiExport.Spec.LatestResourceSchemas = append(apiExport.Spec.LatestResourceSchemas, "foo.bar")
		_, err := kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Update(ctx, apiExport, metav1.UpdateOptions{})
		return err
	})
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Change some metadata on source APIExport %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResource(ctx, t, cluster, func(res runtime.Object) error {
		if err := scenario.ChangeMetadataFor(res); err != nil {
			return err
		}
		apiExport, ok := res.(*apisv1alpha1.APIExport)
		if !ok {
			return fmt.Errorf("%T is not *APIExport", res)
		}
		_, err := kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Update(ctx, apiExport, metav1.UpdateOptions{})
		return err
	})
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Verify that deleting source APIExport %s/%s leads to removal of the cached object", cluster, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t, cluster)
}

// replicateAPIExportNegativeScenario checks if modified or even deleted cached APIExport will be reconciled to match the original object
func replicateAPIExportNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	resourceName := "mangodb"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "APIExport", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source APIExport %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		export := &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceName,
			},
		}
		_, err := kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
		return err
	})
	t.Logf("Verify that the source APIExport %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Delete cached APIExport %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t, cluster)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Update cached APIExport %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateCachedResource(ctx, t, cluster, func(res runtime.Object) error {
		cachedExport, ok := res.(*apisv1alpha1.APIExport)
		if !ok {
			return fmt.Errorf("%T is not *APIExport", res)
		}
		cachedExport.Spec.LatestResourceSchemas = append(cachedExport.Spec.LatestResourceSchemas, "foo")
		_, err := cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), cachedExport, metav1.UpdateOptions{})
		return err
	})
	t.Logf("Verify that the cached APIExport %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)
}

// replicateClusterWorkspaceShardScenario tests if a ClusterWorkspaceShard is propagated to the cache server.
// The test exercises creation, modification and removal of the ClusterWorkspaceShard object.
func replicateClusterWorkspaceShardScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	// The ClusterWorkspaceShard API is per default only available in the root workspace
	cluster := tenancyv1alpha1.RootCluster
	resourceName := "test-shard"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "ClusterWorkspaceShard", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source ClusterWorkspaceShard %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		cws := &tenancyv1alpha1.ClusterWorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: resourceName,
			},
			Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
				BaseURL: "https://base.kcp.test.dev",
			},
		}
		cws, err := kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Create(ctx, cws, metav1.CreateOptions{})
		resourceName = cws.Name
		scenario.resourceName = cws.Name
		return err
	})
	t.Logf("Verify that the source ClusterWorkspaceShard %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Change the spec on source ClusterWorkspaceShard %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResource(ctx, t, cluster, func(res runtime.Object) error {
		cws, ok := res.(*tenancyv1alpha1.ClusterWorkspaceShard)
		if !ok {
			return fmt.Errorf("%T is not *ClusterWorkspaceShard", res)
		}
		cws.Spec.BaseURL = "https://kcp.test.dev"
		_, err := kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Update(ctx, cws, metav1.UpdateOptions{})
		return err
	})
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Change some metadata on source ClusterWorkspaceShard %s/%s and verify if updates were propagated to the cached object", cluster, resourceName)
	scenario.UpdateSourceResource(ctx, t, cluster, func(res runtime.Object) error {
		if err := scenario.ChangeMetadataFor(res); err != nil {
			return err
		}
		cws, ok := res.(*tenancyv1alpha1.ClusterWorkspaceShard)
		if !ok {
			return fmt.Errorf("%T is not *ClusterWorkspaceShard", res)
		}
		_, err := kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Update(ctx, cws, metav1.UpdateOptions{})
		return err
	})
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Verify that deleting source ClusterWorkspaceShard %s/%s leads to removal of the cached object", cluster, resourceName)
	scenario.DeleteSourceResourceAndVerify(ctx, t, cluster)
}

// replicateClusterWorkspaceShardNegativeScenario checks if modified or even deleted cached ClusterWorkspaceShard will be reconciled to match the original object
func replicateClusterWorkspaceShardNegativeScenario(ctx context.Context, t *testing.T, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	// The ClusterWorkspaceShard API is per default only available in the root workspace
	cluster := tenancyv1alpha1.RootCluster
	resourceName := "test-shard"
	scenario := &replicateResourceScenario{resourceName: resourceName, resourceKind: "ClusterWorkspaceShard", server: server, kcpShardClusterClient: kcpShardClusterClient, cacheKcpClusterClient: cacheKcpClusterClient}

	t.Logf("Create source ClusterWorkspaceShard %s/%s on the root shard for replication", cluster, resourceName)
	scenario.CreateSourceResource(t, func() error {
		cws := &tenancyv1alpha1.ClusterWorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: resourceName,
			},
			Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
				BaseURL: "https://base.kcp.test.dev",
			},
		}
		cws, err := kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Create(ctx, cws, metav1.CreateOptions{})
		resourceName = cws.Name
		scenario.resourceName = cws.Name
		return err
	})
	t.Logf("Verify that the source ClusterWorkspaceShard %s/%s was replicated to the cache server", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Delete cached ClusterWorkspaceShard %s/%s and check if it was brought back by the replication controller", cluster, resourceName)
	scenario.DeleteCachedResource(ctx, t, cluster)
	scenario.VerifyReplication(ctx, t, cluster)

	t.Logf("Update cached ClusterWorkspaceShard %s/%s so that it differs from the source resource", cluster, scenario.resourceName)
	scenario.UpdateCachedResource(ctx, t, cluster, func(res runtime.Object) error {
		cachedCws, ok := res.(*tenancyv1alpha1.ClusterWorkspaceShard)
		if !ok {
			return fmt.Errorf("%T is not *ClusterWorkspaceShard", res)
		}
		cachedCws.Spec.BaseURL = "https://base2.kcp.test.dev"
		_, err := cacheKcpClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), cachedCws, metav1.UpdateOptions{})
		return err
	})
	t.Logf("Verify that the cached ClusterWorkspaceShard %s/%s was brought back by the replication controller after an update", cluster, resourceName)
	scenario.VerifyReplication(ctx, t, cluster)
}

// TestCacheServerInProcess runs all test scenarios against a cache server that runs with a kcp server
func TestCacheServerInProcess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile),
			"--run-cache-server=true",
		)...,
		))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardClient, err := kcpclientset.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	cacheClientRT := CacheClientRoundTrippersFor(kcpRootShardConfig)
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, server, kcpRootShardClient, cacheKcpClusterClient)
		})
	}
}

// TestCacheServerStandalone runs all test scenarios against a standalone cache server
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
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile), "--run-cache-server=true", fmt.Sprintf("--cache-server-kubeconfig-file=%s", cacheKubeconfigPath))...),
		framework.WithScratchDirectories(artifactDir, dataDir),
	)
	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardClient, err := kcpclientset.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)

	cacheServerKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
	require.NoError(t, err)
	cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(*cacheServerKubeConfig, "cache", nil, nil)
	cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
	require.NoError(t, err)
	cacheClientRT := CacheClientRoundTrippersFor(cacheClientRestConfig)
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			scenario.work(ctx, t, server, kcpRootShardClient, cacheKcpClusterClient)
		})
	}
}

// replicateResourceScenario an auxiliary struct that is used by all test scenarios defined in this pkg
type replicateResourceScenario struct {
	resourceName string
	resourceKind string

	server                framework.RunningServer
	kcpShardClusterClient kcpclientset.ClusterInterface
	cacheKcpClusterClient kcpclientset.ClusterInterface
}

func (b *replicateResourceScenario) CreateSourceResource(t *testing.T, createSourceResource func() error) {
	require.NoError(t, createSourceResource())
}

func (b *replicateResourceScenario) UpdateSourceResource(ctx context.Context, t *testing.T, cluster logicalcluster.Path, updater func(runtime.Object) error) {
	b.resourceUpdateHelper(ctx, t, cluster, b.getSourceResourceHelper, updater)
}

func (b *replicateResourceScenario) UpdateCachedResource(ctx context.Context, t *testing.T, cluster logicalcluster.Path, updater func(runtime.Object) error) {
	b.resourceUpdateHelper(ctx, t, cluster, b.getCachedResourceHelper, updater)
}

func (b *replicateResourceScenario) DeleteSourceResourceAndVerify(ctx context.Context, t *testing.T, cluster logicalcluster.Path) {
	require.NoError(t, b.deleteSourceResourceHelper(ctx, cluster))
	framework.Eventually(t, func() (bool, string) {
		_, err := b.getCachedResourceHelper(ctx, cluster)
		if errors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("replicated %s %s/%s wasn't removed", b.resourceKind, cluster, b.resourceName)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) DeleteCachedResource(ctx context.Context, t *testing.T, cluster logicalcluster.Path) {
	err := b.deleteCachedResource(ctx, cluster)
	require.NoError(t, err)
}

func (b *replicateResourceScenario) VerifyReplication(ctx context.Context, t *testing.T, cluster logicalcluster.Path) {
	b.verifyResourceReplicationHelper(ctx, t, cluster)
}

func (b *replicateResourceScenario) ChangeMetadataFor(originalResource runtime.Object) error {
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

func (b *replicateResourceScenario) resourceUpdateHelper(ctx context.Context, t *testing.T, cluster logicalcluster.Path, resourceGetter func(ctx context.Context, cluster logicalcluster.Path) (runtime.Object, error), resourceUpdater func(runtime.Object) error) {
	framework.Eventually(t, func() (bool, string) {
		resource, err := resourceGetter(ctx, cluster)
		if err != nil {
			return false, err.Error()
		}
		err = resourceUpdater(resource)
		if err != nil {
			if !errors.IsConflict(err) {
				return false, fmt.Sprintf("unknown error while updating the cached %s/%s/%s, err: %s", b.resourceKind, cluster, b.resourceName, err.Error())
			}
			return false, err.Error() // try again
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) verifyResourceReplicationHelper(ctx context.Context, t *testing.T, cluster logicalcluster.Path) {
	t.Helper()
	t.Logf("Get %s %s/%s from the root shard and the cache server for comparison", b.resourceKind, cluster, b.resourceName)
	framework.Eventually(t, func() (bool, string) {
		originalResource, err := b.getSourceResourceHelper(ctx, cluster)
		if err != nil {
			return false, err.Error()
		}
		cachedResource, err := b.getCachedResourceHelper(ctx, cluster)
		if err != nil {
			if !errors.IsNotFound(err) {
				return true, err.Error()
			}
			return false, err.Error()
		}
		t.Logf("Compare if both the original and replicated resources (%s %s/%s) are the same except %s annotation and ResourceVersion", b.resourceKind, cluster, b.resourceName, genericapirequest.AnnotationKey)
		cachedResourceMeta, err := meta.Accessor(cachedResource)
		if err != nil {
			return false, err.Error()
		}
		if _, found := cachedResourceMeta.GetAnnotations()[genericapirequest.AnnotationKey]; !found {
			t.Fatalf("replicated %s root|%s/%s, doesn't have %s annotation", b.resourceKind, cluster, cachedResourceMeta.GetName(), genericapirequest.AnnotationKey)
		}
		delete(cachedResourceMeta.GetAnnotations(), genericapirequest.AnnotationKey)
		if diff := cmp.Diff(cachedResource, originalResource, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); len(diff) > 0 {
			return false, fmt.Sprintf("replicated %s root|%s/%s is different from the original", b.resourceKind, cluster, cachedResourceMeta.GetName())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func (b *replicateResourceScenario) getSourceResourceHelper(ctx context.Context, cluster logicalcluster.Path) (runtime.Object, error) {
	switch b.resourceKind {
	case "APIExport":
		return b.kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(ctx, b.resourceName, metav1.GetOptions{})
	case "APIResourceSchema":
		return b.kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Get(ctx, b.resourceName, metav1.GetOptions{})
	case "ClusterWorkspaceShard":
		return b.kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, b.resourceName, metav1.GetOptions{})
	}
	return nil, fmt.Errorf("unable to get a REST client for an unknown %s Kind", b.resourceKind)
}

func (b *replicateResourceScenario) getCachedResourceHelper(ctx context.Context, cluster logicalcluster.Path) (runtime.Object, error) {
	switch b.resourceKind {
	case "APIExport":
		return b.cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	case "APIResourceSchema":
		return b.cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	case "ClusterWorkspaceShard":
		return b.cacheKcpClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.GetOptions{})
	}
	return nil, fmt.Errorf("unable to get a REST client for an unknown %s Kind", b.resourceKind)
}

func (b *replicateResourceScenario) deleteSourceResourceHelper(ctx context.Context, cluster logicalcluster.Path) error {
	switch b.resourceKind {
	case "APIExport":
		return b.kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Delete(ctx, b.resourceName, metav1.DeleteOptions{})
	case "APIResourceSchema":
		return b.kcpShardClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Delete(ctx, b.resourceName, metav1.DeleteOptions{})
	case "ClusterWorkspaceShard":
		return b.kcpShardClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Delete(ctx, b.resourceName, metav1.DeleteOptions{})
	}
	return fmt.Errorf("unable to get a REST client for an unknown %s Kind", b.resourceKind)
}

func (b *replicateResourceScenario) deleteCachedResource(ctx context.Context, cluster logicalcluster.Path) error {
	switch b.resourceKind {
	case "APIExport":
		return b.cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.DeleteOptions{})
	case "APIResourceSchema":
		return b.cacheKcpClusterClient.Cluster(cluster).ApisV1alpha1().APIResourceSchemas().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.DeleteOptions{})
	case "ClusterWorkspaceShard":
		return b.cacheKcpClusterClient.Cluster(cluster).TenancyV1alpha1().ClusterWorkspaceShards().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), b.resourceName, metav1.DeleteOptions{})
	}
	return fmt.Errorf("unable to get a REST client for an unknown %s Kind", b.resourceKind)
}
