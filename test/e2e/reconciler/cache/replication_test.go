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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/clientcmd"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testScenario struct {
	name string
	work func(ctx context.Context, t *testing.T, scenario *replicateResourceScenario, server framework.RunningServer, kcpShardClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface)
	init func(t *testing.T) *replicateResourceScenario
}

// scenarios contains all test scenarios that will be run against in-process and standalone cache server
// to cover additional resources, add a test scenario here and create an init function.
var scenarios = []testScenario{
	{"TestReplicateAPIExport", replicateScenario, initAPIExports},
	{"TestReplicateAPIExportNegative", replicateNegativeScenario, initAPIExports},
	{"TestReplicateAPIResourceSchema", replicateScenario, initAPIResourceSchemas},
	{"TestReplicateAPIResourceSchemaNegative", replicateNegativeScenario, initAPIResourceSchemas},
	{"TestReplicateShard", replicateScenario, initShards},
	{"TestReplicateShardNegative", replicateNegativeScenario, initShards},
}

// initAPIExports initializes the scenario with some data and functions handling API calls specific to  the resource.
func initAPIExports(t *testing.T) *replicateResourceScenario {
	t.Helper()
	return &replicateResourceScenario{
		// A new org and workspace get created if none is specified.
		clusterName:  "",
		resourceName: "wild.wild.west",
		resourceKind: "APIExport",

		create: func(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface, scenario *replicateResourceScenario) error {
			t.Helper()
			apifixtures.CreateSheriffsSchemaAndExport(ctx, t, clusterName.Path(), client, "wild.wild.west", "testing replication to the cache server")
			return nil
		},

		getSource: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Get(ctx, resourceName, metav1.GetOptions{})
		},

		getCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.GetOptions{})
		},

		delete: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Delete(ctx, resourceName, metav1.DeleteOptions{})
		},

		deleteCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.DeleteOptions{})
		},

		updateSpec: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			apiExport, ok := object.(*apisv1alpha1.APIExport)
			if !ok {
				return fmt.Errorf("%T is not *APIExport", object)
			}
			apiExport.Spec.LatestResourceSchemas = append(apiExport.Spec.LatestResourceSchemas, "foo.bar")
			_, err := client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Update(ctx, apiExport, metav1.UpdateOptions{})
			return err
		},

		updateMetadata: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			if err := changeMetadataFor(t, object); err != nil {
				return err
			}
			apiExport, ok := object.(*apisv1alpha1.APIExport)
			if !ok {
				return fmt.Errorf("%T is not *APIExport", object)
			}
			_, err := client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Update(ctx, apiExport, metav1.UpdateOptions{})
			return err
		},

		updateCached: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			cachedAPIExport, ok := object.(*apisv1alpha1.APIExport)
			if !ok {
				return fmt.Errorf("%T is not *APIExport", object)
			}
			cachedAPIExport.Spec.LatestResourceSchemas = append(cachedAPIExport.Spec.LatestResourceSchemas, "foo")
			_, err := client.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), cachedAPIExport, metav1.UpdateOptions{})
			return err
		},
	}
}

// initAPIResourceSchemas initializes the scenario with some data and functions handling API calls specific to  the resource.
func initAPIResourceSchemas(t *testing.T) *replicateResourceScenario {
	t.Helper()
	return &replicateResourceScenario{
		// A new org and workspace get created if none is specified.
		clusterName:  "",
		resourceName: "today.sheriffs.wild.wild.west",
		resourceKind: "APIResourceSchema",

		create: func(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface, scenario *replicateResourceScenario) error {
			t.Helper()
			apifixtures.CreateSheriffsSchemaAndExport(ctx, t, clusterName.Path(), client, "wild.wild.west", "testing replication to the cache server")
			return nil
		},

		getSource: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(ctx, resourceName, metav1.GetOptions{})
		},

		getCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.GetOptions{})
		},

		delete: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Delete(ctx, resourceName, metav1.DeleteOptions{})
		},

		deleteCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.DeleteOptions{})
		},

		// note that since the spec of an APIResourceSchema is immutable we are limited to changing some metadata
		updateSpec: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			return nil
		},

		updateMetadata: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			if err := changeMetadataFor(t, object); err != nil {
				return err
			}
			apiResSchema, ok := object.(*apisv1alpha1.APIResourceSchema)
			if !ok {
				return fmt.Errorf("%T is not *APIResourceSchema", object)
			}
			_, err := client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Update(ctx, apiResSchema, metav1.UpdateOptions{})
			return err
		},

		updateCached: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			cachedSchema, ok := object.(*apisv1alpha1.APIResourceSchema)
			if !ok {
				return fmt.Errorf("%T is not *APIResourceSchema", object)
			}
			// since the spec of an APIResourceSchema is immutable
			// let's modify some metadata
			if cachedSchema.Labels == nil {
				cachedSchema.Labels = map[string]string{}
			}
			cachedSchema.Labels["foo"] = "bar"
			_, err := client.Cluster(clusterName.Path()).ApisV1alpha1().APIResourceSchemas().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), cachedSchema, metav1.UpdateOptions{})
			return err
		},
	}
}

// initShards initializes the scenario with some data and functions handling API calls specific to  the resource.
func initShards(t *testing.T) *replicateResourceScenario {
	t.Helper()
	return &replicateResourceScenario{
		// TheShard API is per default only available in the root workspace.
		clusterName:  core.RootCluster,
		resourceName: "test-shard",
		resourceKind: "Shard",

		create: func(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface, scenario *replicateResourceScenario) error {
			t.Helper()
			s := &corev1alpha1.Shard{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: resourceName,
				},
				Spec: corev1alpha1.ShardSpec{
					BaseURL: "https://base.kcp.test.dev",
				},
			}
			resource, err := client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Create(ctx, s, metav1.CreateOptions{})
			if err != nil {
				return err
			}
			scenario.resourceName = resource.Name
			return nil
		},

		getSource: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Get(ctx, resourceName, metav1.GetOptions{})
		},

		getCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error) {
			return client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Get(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.GetOptions{})
		},

		delete: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Delete(ctx, resourceName, metav1.DeleteOptions{})
		},

		deleteCached: func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error {
			return client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Delete(cacheclient.WithShardInContext(ctx, shard.New("root")), resourceName, metav1.DeleteOptions{})
		},

		updateSpec: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			s, ok := object.(*corev1alpha1.Shard)
			if !ok {
				return fmt.Errorf("%T is not *Shard", object)
			}
			s.Spec.BaseURL = "https://kcp.test.dev"
			_, err := client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Update(ctx, s, metav1.UpdateOptions{})
			return err
		},

		updateMetadata: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			if err := changeMetadataFor(t, object); err != nil {
				return err
			}
			s, ok := object.(*corev1alpha1.Shard)
			if !ok {
				return fmt.Errorf("%T is not *Shard", object)
			}
			_, err := client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Update(ctx, s, metav1.UpdateOptions{})
			return err
		},

		updateCached: func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error {
			s, ok := object.(*corev1alpha1.Shard)
			if !ok {
				return fmt.Errorf("%T is not *Shard", object)
			}
			s.Spec.BaseURL = "https://base2.kcp.test.dev"
			_, err := client.Cluster(clusterName.Path()).CoreV1alpha1().Shards().Update(cacheclient.WithShardInContext(ctx, shard.New("root")), s, metav1.UpdateOptions{})
			return err
		},
	}
}

// replicateScenario tests if a resource is propagated to the cache server.
// The test exercises creation, modification and removal of the resource object.
func replicateScenario(ctx context.Context, t *testing.T, scenario *replicateResourceScenario, server framework.RunningServer, kcpClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	t.Helper()
	if scenario.clusterName.Empty() {
		org := framework.NewOrganizationFixture(t, server)
		scenario.clusterName = framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	}

	t.Logf("Create source %s %s/%s on the root shard for replication", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	require.NoError(t, scenario.create(ctx, t, scenario.clusterName, scenario.resourceName, kcpClusterClient, scenario))

	t.Logf("Verify that the source %s %s/%s was replicated to the cache server", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)

	t.Logf("Change the spec on source %s %s/%s and verify if updates were propagated to the cached object", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	resourceUpdate(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.updateSpec, kcpClusterClient)
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)

	t.Logf("Change some metadata on source %s %s/%s and verify if updates were propagated to the cached object", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	resourceUpdate(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.updateMetadata, kcpClusterClient)
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)

	t.Logf("Verify that deleting source %s %s/%s leads to removal of the cached object", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	deleteSourceResourceAndVerify(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getCached, scenario.delete, kcpClusterClient)
}

// replicateNegativeScenario checks if modified or even deleted cached resources get reconciled to match the original object.
func replicateNegativeScenario(ctx context.Context, t *testing.T, scenario *replicateResourceScenario, server framework.RunningServer, kcpClusterClient kcpclientset.ClusterInterface, cacheKcpClusterClient kcpclientset.ClusterInterface) {
	t.Helper()
	if scenario.clusterName.Empty() {
		org := framework.NewOrganizationFixture(t, server)
		scenario.clusterName = framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	}

	t.Logf("Create source %s %s/%s on the root shard for replication", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	require.NoError(t, scenario.create(ctx, t, scenario.clusterName, scenario.resourceName, kcpClusterClient, scenario))

	t.Logf("Verify that the source %s %s/%s was replicated to the cache server", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)

	t.Logf("Delete cached %s %s/%s and check if it was brought back by the replication controller", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	require.NoError(t, scenario.deleteCached(ctx, scenario.clusterName, scenario.resourceName, cacheKcpClusterClient))
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)

	t.Logf("Update cached %s %s/%s so that it differs from the source resource", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	resourceUpdate(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getCached, scenario.updateCached, cacheKcpClusterClient)
	t.Logf("Verify that the cached %s %s/%s was brought back by the replication controller after an update", scenario.resourceKind, scenario.clusterName, scenario.resourceName)
	verifyResourceReplication(ctx, t, scenario.clusterName, scenario.resourceKind, scenario.resourceName, scenario.getSource, scenario.getCached, kcpClusterClient, cacheKcpClusterClient)
}

// TestCacheServerInProcess runs all test scenarios against a cache server that runs with a kcp server.
func TestCacheServerInProcess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default.
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)...))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpRootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpRootShardClient, err := kcpclientset.NewForConfig(kcpRootShardConfig)
	require.NoError(t, err)
	cacheClientRT := ClientRoundTrippersFor(kcpRootShardConfig)
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, testScenario := range scenarios {
		testScenario := testScenario
		scenario := testScenario.init(t)
		t.Run(testScenario.name, func(t *testing.T) {
			t.Parallel()
			testScenario.work(ctx, t, scenario, server, kcpRootShardClient, cacheKcpClusterClient)
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

	// TODO(p0lyn0mial): switch to framework.SharedKcpServer when caching is turned on by default.
	tokenAuthFile := framework.WriteTokenAuthFile(t)
	server := framework.PrivateKcpServer(t,
		framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile), fmt.Sprintf("--cache-server-kubeconfig-file=%s", cacheKubeconfigPath))...),
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
	cacheClientRT := ClientRoundTrippersFor(cacheClientRestConfig)
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientRT)
	require.NoError(t, err)

	for _, testScenario := range scenarios {
		testScenario := testScenario
		scenario := testScenario.init(t)
		t.Run(testScenario.name, func(t *testing.T) {
			t.Parallel()
			testScenario.work(ctx, t, scenario, server, kcpRootShardClient, cacheKcpClusterClient)
		})
	}
}

// replicateResourceScenario an auxiliary struct that is used by all test scenarios defined in this pkg.
type replicateResourceScenario struct {
	resourceName   string
	resourceKind   string
	clusterName    logicalcluster.Name
	create         createFunc
	getSource      resourceFunc
	getCached      resourceFunc
	updateSpec     updateFunc
	updateMetadata updateFunc
	updateCached   updateFunc
	delete         deleteFunc
	deleteCached   deleteFunc
}

type createFunc func(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface, scenario *replicateResourceScenario) error
type resourceFunc func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) (runtime.Object, error)
type updateFunc func(ctx context.Context, object runtime.Object, clusterName logicalcluster.Name, client kcpclientset.ClusterInterface) error
type deleteFunc func(ctx context.Context, clusterName logicalcluster.Name, resourceName string, client kcpclientset.ClusterInterface) error

func deleteSourceResourceAndVerify(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceKind string, resourceName string,
	get resourceFunc, delete deleteFunc, client kcpclientset.ClusterInterface) {
	t.Helper()
	require.NoError(t, delete(ctx, clusterName, resourceName, client))
	framework.Eventually(t, func() (bool, string) {
		_, err := get(ctx, clusterName, resourceName, client)
		if errors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("replicated %s %s/%s wasn't removed", resourceKind, clusterName, resourceName)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func changeMetadataFor(t *testing.T, originalResource runtime.Object) error {
	t.Helper()
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

func resourceUpdate(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceKind string, resourceName string,
	get resourceFunc, update updateFunc, client kcpclientset.ClusterInterface) {
	t.Helper()
	framework.Eventually(t, func() (bool, string) {
		resource, err := get(ctx, clusterName, resourceName, client)
		if err != nil {
			return false, err.Error()
		}
		err = update(ctx, resource, clusterName, client)
		if err != nil {
			if !errors.IsConflict(err) {
				return false, fmt.Sprintf("unknown error while updating %s/%s/%s, err: %s", resourceKind, clusterName, resourceName, err.Error())
			}
			return false, err.Error() // try again
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func verifyResourceReplication(ctx context.Context, t *testing.T, clusterName logicalcluster.Name, resourceKind string, resourceName string,
	getSourceResource resourceFunc, getCachedResource resourceFunc, kcpClient kcpclientset.ClusterInterface, cacheClient kcpclientset.ClusterInterface) {
	t.Helper()
	t.Logf("Get %s %s/%s from the root shard and the cache server for comparison", resourceKind, clusterName, resourceName)
	framework.Eventually(t, func() (bool, string) {
		originalResource, err := getSourceResource(ctx, clusterName, resourceName, kcpClient)
		if err != nil {
			return false, err.Error()
		}
		cachedResource, err := getCachedResource(ctx, clusterName, resourceName, cacheClient)
		if err != nil {
			if !errors.IsNotFound(err) {
				return true, err.Error()
			}
			return false, err.Error()
		}
		t.Logf("Compare if both the original and replicated resources (%s %s/%s) are the same except %s annotation and ResourceVersion", resourceKind, clusterName, resourceName, genericapirequest.AnnotationKey)
		cachedResourceMeta, err := meta.Accessor(cachedResource)
		if err != nil {
			return false, err.Error()
		}
		if _, found := cachedResourceMeta.GetAnnotations()[genericapirequest.AnnotationKey]; !found {
			t.Fatalf("replicated %s root|%s/%s, doesn't have %s annotation", resourceKind, clusterName, cachedResourceMeta.GetName(), genericapirequest.AnnotationKey)
		}
		delete(cachedResourceMeta.GetAnnotations(), genericapirequest.AnnotationKey)
		if diff := cmp.Diff(cachedResource, originalResource, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); len(diff) > 0 {
			return false, fmt.Sprintf("replicated %s root|%s/%s is different from the original", resourceKind, clusterName, cachedResourceMeta.GetName())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
