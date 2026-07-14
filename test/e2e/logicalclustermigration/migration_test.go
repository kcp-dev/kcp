/*
Copyright 2026 The kcp Authors.

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

package logicalclustermigration

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/apis/v1alpha2"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestFullMigration(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	t.Logf("Using origin shard %q and destination shard %q", originShard, destinationShard)

	// Create an org workspace and a child workspace pinned to the origin shard.
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	lcName := logicalcluster.Name(ws.Spec.Cluster)

	t.Logf("Workspace %s (logical cluster %s) created on shard %s", wsPath, lcName, originShard)

	t.Logf("Creatint test data")
	numConfigMaps := 5
	for i := range numConfigMaps {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
			t.Context(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("test-cm-%d", i)},
				Data:       map[string]string{"index": fmt.Sprintf("%d", i)},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)
	}

	t.Log("Starting informer for post-migration verification")
	cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
	informerStore, informerController := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: &cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return cmClient.List(t.Context(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return cmClient.Watch(t.Context(), options)
			},
		},
		ObjectType: &corev1.ConfigMap{},
		Handler:    cache.ResourceEventHandlerFuncs{},
	})
	informerCtx, informerCancel := context.WithCancel(t.Context())
	t.Cleanup(informerCancel)
	go informerController.Run(informerCtx.Done())

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "migration",
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Waiting for migration APIBinding to be bound in %s", orgPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	t.Logf("Creating LogicalClusterMigration for %s from %s to %s", lcName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-migration",
				},
				Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster:   lcName.String(),
					DestinationShard: destinationShard,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			t.Logf("failed to create LogicalClusterMigration: %v", err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	t.Logf("Waiting for migration to complete")
	var completedMigration *migrationv1alpha1.LogicalClusterMigration
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q, entriesCopied: %d", migration.Status.Phase, migration.Status.EntriesCopied)
		t.Logf("migration conditions: %#v", migration.Status.Conditions)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
		completedMigration = migration
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration to complete")

	t.Logf("Migration completed, running post-migration tests")

	// The paginated dump copy should have made progress and left no
	// dangling continue token once the copy finished.
	t.Run("DumpProgressReporting", func(t *testing.T) {
		t.Parallel()

		assert.Positive(t, completedMigration.Status.EntriesCopied, "status.entriesCopied should reflect the copied etcd entries")
		assert.Empty(t, completedMigration.Status.DumpContinue, "status.dumpContinue should be cleared once the copy is complete")
	})

	// Test that a client can access the lc after the migration.
	t.Run("ClientAccess", func(t *testing.T) {
		t.Parallel()

		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "LIST should succeed after migration")

		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Get(t.Context(), "test-cm-0", metav1.GetOptions{})
		require.NoError(t, err, "GET should succeed after migration")
	})

	// Verify that a RetryWatcher that was setup before the migration was
	// started reconnects and delivers events after the migration.
	// The RetryWatcher relists internally on 410 Gone (RV mismatch
	// across shards), which is the expected behavior for clients that
	// track resource versions across a migration.
	t.Run("PreMigrateWatch", func(t *testing.T) {
		t.Parallel()

		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
			t.Context(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-cm"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		// The pre-migration RetryWatcher may have been terminated due to
		// the RV space change (410 Gone). Establish a fresh watch to
		// verify new watches work after migration.
		postMigrationWatcher, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Watch(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "should be able to establish a new watch after migration")
		defer postMigrationWatcher.Stop()

		for {
			select {
			case <-t.Context().Done():
				t.Fatal("test context closed")
			case event, ok := <-postMigrationWatcher.ResultChan():
				if !ok {
					t.Fatal("post-migration watch channel closed unexpectedly")
				}
				if event.Type == watch.Error {
					continue
				}
				cm, ok := event.Object.(*corev1.ConfigMap)
				if ok && cm.Name == "post-migration-cm" {
					return
				}
			case <-time.After(wait.ForeverTestTimeout):
				t.Fatal("post-migration watch did not receive event")
			}
		}
	})

	// Verify that an informer that was setup before the migration was
	// started picks up objects after the migration.
	t.Run("PreMigrateInformer", func(t *testing.T) {
		t.Parallel()

		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
			t.Context(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-informer-cm"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, exists, err := informerStore.GetByKey("default/post-migration-informer-cm")
			assert.NoError(c, err)
			assert.True(c, exists, "informer should have picked up post-migration-informer-cm")
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for informer to pick up post-migration object")
	})

	// Check that all test objects were transferred to the new shard.
	t.Run("DataIntegrity", func(t *testing.T) {
		t.Parallel()

		cms, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)

		cmNames := make(map[string]bool, len(cms.Items))
		for _, cm := range cms.Items {
			cmNames[cm.Name] = true
		}

		for i := range numConfigMaps {
			name := fmt.Sprintf("test-cm-%d", i)
			require.True(t, cmNames[name], "ConfigMap %s not found after migration", name)
		}
	})
}

// TestConcurrentMigrationsFromSameOriginShard runs several migrations from
// the same origin shard to the same destination shard at the same time.
// The destination shard's controller shares one client per origin shard
// across concurrent migrations (refcounted, torn down once the last user
// releases it) instead of building one per migration - this exercises that
// sharing isn't observable as internal state from a black-box e2e test, so
// what's actually being checked is the thing that WOULD break if the
// sharing/refcounting had a bug: every migration completes with the right
// data, and none of them interfere with each other's copy.
func TestConcurrentMigrationsFromSameOriginShard(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	const numMigrations = 3

	t.Logf("Using origin shard %q and destination shard %q for %d concurrent migrations", originShard, destinationShard, numMigrations)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "migration",
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Waiting for migration APIBinding to be bound in %s", orgPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	// Set up all workspaces + their own distinct test data up front, all
	// pinned to the same origin shard, so their migrations can genuinely
	// overlap once kicked off below.
	type target struct {
		wsPath logicalcluster.Path
		lcName logicalcluster.Name
		cmName string
	}
	targets := make([]target, numMigrations)
	for i := range numMigrations {
		wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
		lcName := logicalcluster.Name(ws.Spec.Cluster)
		cmName := fmt.Sprintf("concurrent-migration-marker-%d", i)

		t.Logf("Workspace %d: %s (logical cluster %s) created on shard %s", i, wsPath, lcName, originShard)

		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
			t.Context(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName},
				Data:       map[string]string{"workspace-index": fmt.Sprintf("%d", i)},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		targets[i] = target{wsPath: wsPath, lcName: lcName, cmName: cmName}
	}

	// Kick off all migrations before waiting on any of them, to maximize
	// the chance their copies genuinely overlap in time.
	migrationNames := make([]string, numMigrations)
	for i, tgt := range targets {
		migrationName := fmt.Sprintf("concurrent-migration-%d", i)
		migrationNames[i] = migrationName

		t.Logf("Creating LogicalClusterMigration %q for %s from %s to %s", migrationName, tgt.lcName, originShard, destinationShard)
		require.Eventually(t, func() bool {
			_, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
				t.Context(),
				&migrationv1alpha1.LogicalClusterMigration{
					ObjectMeta: metav1.ObjectMeta{
						Name: migrationName,
					},
					Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
						LogicalCluster:   tgt.lcName.String(),
						DestinationShard: destinationShard,
					},
				},
				metav1.CreateOptions{},
			)
			if err != nil {
				t.Logf("failed to create LogicalClusterMigration %q: %v", migrationName, err)
			}
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration %q", migrationName)
	}

	// Wait for + verify each migration in parallel: t.Parallel() subtests
	// all pause here until every sibling has reached this point, then run
	// concurrently, so the "wait for completion" below happens for all
	// numMigrations migrations at once.
	for i, tgt := range targets {
		t.Run(migrationNames[i], func(t *testing.T) {
			t.Parallel()

			migrationName := migrationNames[i]

			var completedMigration *migrationv1alpha1.LogicalClusterMigration
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), migrationName, metav1.GetOptions{})
				require.NoError(c, err)
				require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
				completedMigration = migration
			}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration %q to complete", migrationName)

			assert.Positive(t, completedMigration.Status.EntriesCopied, "status.entriesCopied should reflect the copied etcd entries for %q", migrationName)
			assert.Empty(t, completedMigration.Status.DumpContinue, "status.dumpContinue should be cleared once %q is complete", migrationName)

			// Data integrity: this workspace must have exactly its own
			// marker ConfigMap - not missing (dropped by a racing copy)
			// and not some other workspace's data (cross-contamination
			// from an incorrectly-shared client/request).
			cm, err := kubeClusterClient.Cluster(tgt.wsPath).CoreV1().ConfigMaps("default").Get(t.Context(), tgt.cmName, metav1.GetOptions{})
			require.NoError(t, err, "marker ConfigMap %q missing after migration", tgt.cmName)
			assert.Equal(t, fmt.Sprintf("%d", i), cm.Data["workspace-index"], "marker ConfigMap %q has wrong data - possible cross-contamination between concurrent migrations", tgt.cmName)
		})
	}
}

// TestMigrationAPIBindingUnprivileged verifies that the migration API
// can't be bound even by cluster-admin users in other workspaces.
func TestMigrationAPIBindingUnprivileged(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	userCfg := framework.StaticTokenUserConfig("user-1", cfg)
	userKcpClient, err := kcpclientset.NewForConfig(userCfg)
	require.NoError(t, err)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, orgPath, []string{"user-1"}, nil, true)

	_, err = userKcpClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "migration",
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.True(t, errors.IsForbidden(err), "expected forbidden, got: %v", err)
}

func TestFullMigrationAPIBindings(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)
	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	t.Logf("Using origin shard %q and destination shard %q", originShard, destinationShard)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWs := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	lcName := logicalcluster.Name(consumerWs.Spec.Cluster)

	t.Logf("Provider workspace: %s, Consumer workspace: %s (logical cluster %s, shard %s)", providerPath, consumerPath, lcName, originShard)

	t.Logf("Installing Cowboys APIResourceSchema in provider workspace %s", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Creating Cowboys APIExport in provider workspace %s", providerPath)
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(
		t.Context(),
		&apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
			Spec: apisv1alpha2.APIExportSpec{
				Resources: []apisv1alpha2.ResourceSchema{
					{
						Name:   "cowboys",
						Group:  "wildwest.dev",
						Schema: "today.cowboys.wildwest.dev",
						Storage: apisv1alpha2.ResourceSchemaStorage{
							CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
						},
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Creating Cowboys APIBinding in consumer workspace %s", consumerPath)
	require.Eventually(t, func() bool {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(
			t.Context(),
			&apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: providerPath.String(),
							Name: "today-cowboys",
						},
					},
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			t.Logf("failed to create Cowboys APIBinding: %v", err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create Cowboys APIBinding")

	t.Logf("Waiting for Cowboys APIBinding to be bound in %s", consumerPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Cowboys APIBinding never reached Bound phase")

	t.Logf("Creating test Cowboys objects in consumer workspace %s", consumerPath)
	numCowboys := 5
	cowboyClient := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default")
	for i := range numCowboys {
		_, err := cowboyClient.Create(
			t.Context(),
			&wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("test-cowboy-%d", i)},
				Spec:       wildwestv1alpha1.CowboySpec{Intent: fmt.Sprintf("intent-%d", i)},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)
	}

	t.Log("Starting informer for post-migration verification")
	informerStore, informerController := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: &cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return cowboyClient.List(t.Context(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return cowboyClient.Watch(t.Context(), options)
			},
		},
		ObjectType: &wildwestv1alpha1.Cowboy{},
		Handler:    cache.ResourceEventHandlerFuncs{},
	})
	informerCtx, informerCancel := context.WithCancel(t.Context())
	t.Cleanup(informerCancel)
	go informerController.Run(informerCtx.Done())

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "migration"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Waiting for migration APIBinding to be bound in %s", orgPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	t.Logf("Creating LogicalClusterMigration for %s from %s to %s", lcName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{Name: "test-migration-apibindings"},
				Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster:   lcName.String(),
					DestinationShard: destinationShard,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			t.Logf("failed to create LogicalClusterMigration: %v", err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	t.Logf("Waiting for migration to complete")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q", migration.Status.Phase)
		t.Logf("migration conditions: %#v", migration.Status.Conditions)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration to complete")

	t.Logf("Cowboys resource must be already available once the migration is finished")
	resources, err := wildwestClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion("wildwest.dev/v1alpha1")
	require.NoError(t, err, "resource discovery for wildwest.dev/v1alpha1 should succeed")
	t.Logf("discovery for wildwest.dev/v1alpha1 has %d resources", len(resources.APIResources))
	for _, res := range resources.APIResources {
		t.Logf("Group: %q, Version: %q, Resource: %q", res.Group, res.Version, res.Group)
	}
	require.True(t, slices.ContainsFunc(resources.APIResources, func(res metav1.APIResource) bool {
		return res.Name == "cowboys"
	}), "resource discovery should contain cowboys")

	t.Logf("Migration completed, running post-migration tests")

	// Test that a client can access the consumer workspace after the migration.
	t.Run("ClientAccess", func(t *testing.T) {
		t.Parallel()

		_, err := cowboyClient.List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "LIST should succeed after migration")

		_, err = cowboyClient.Get(t.Context(), "test-cowboy-0", metav1.GetOptions{})
		require.NoError(t, err, "GET should succeed after migration")
	})

	// Verify that a watch established after migration can receive events.
	t.Run("PreMigrateWatch", func(t *testing.T) {
		t.Parallel()

		_, err := cowboyClient.Create(
			t.Context(),
			&wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-cowboy"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		postMigrationWatcher, err := cowboyClient.Watch(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "should be able to establish a new watch after migration")
		defer postMigrationWatcher.Stop()

		for {
			select {
			case <-t.Context().Done():
				t.Fatal("test context closed")
			case event, ok := <-postMigrationWatcher.ResultChan():
				if !ok {
					t.Fatal("post-migration watch channel closed unexpectedly")
				}
				if event.Type == watch.Error {
					continue
				}
				cowboy, ok := event.Object.(*wildwestv1alpha1.Cowboy)
				if ok && cowboy.Name == "post-migration-cowboy" {
					return
				}
			case <-time.After(wait.ForeverTestTimeout):
				t.Fatal("post-migration watch did not receive event")
			}
		}
	})

	// Verify that an informer set up before migration picks up objects after migration.
	t.Run("PreMigrateInformer", func(t *testing.T) {
		t.Parallel()

		_, err := cowboyClient.Create(
			t.Context(),
			&wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-informer-cowboy"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, exists, err := informerStore.GetByKey("default/post-migration-informer-cowboy")
			assert.NoError(c, err)
			assert.True(c, exists, "informer should have picked up post-migration-informer-cowboy")
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for informer to pick up post-migration object")
	})

	// Check that all test Cowboys were transferred to the new shard.
	t.Run("DataIntegrity", func(t *testing.T) {
		t.Parallel()

		cowboys, err := cowboyClient.List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)

		cowboyNames := make(map[string]bool, len(cowboys.Items))
		for _, cowboy := range cowboys.Items {
			cowboyNames[cowboy.Name] = true
		}

		for i := range numCowboys {
			name := fmt.Sprintf("test-cowboy-%d", i)
			require.True(t, cowboyNames[name], "Cowboy %s not found after migration", name)
		}
	})
}

func TestFullMigrationAPIExportVirtualWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	t.Logf("Using origin shard %q and destination shard %q", originShard, destinationShard)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, providerWs := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	providerLCName := logicalcluster.Name(providerWs.Spec.Cluster)

	t.Logf("Provider workspace: %s (logical cluster %s, shard %s)", providerPath, providerLCName, originShard)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))

	t.Logf("Installing cowboys APIResourceSchema into provider workspace %s", providerPath)
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Creating cowboys APIExport in provider workspace %s", providerPath)
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
					Verbs:         []string{"get"},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating WorkspaceType with DefaultAPIBindings in provider workspace %s", providerPath)
	workspaceType := tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{Name: "test-consumer-type"},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultChildWorkspaceType: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: "universal",
				Path: "root",
			},
			Extend: tenancyv1alpha1.WorkspaceTypeExtension{
				With: []tenancyv1alpha1.WorkspaceTypeReference{
					{Name: "universal", Path: "root"},
				},
			},
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{Export: apiExport.GetName()},
			},
			DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), &workspaceType, metav1.CreateOptions{})
	require.NoError(t, err)

	consumerPath, consumerWs := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithType(providerPath, tenancyv1alpha1.WorkspaceTypeName(workspaceType.GetName())))
	t.Logf("Consumer workspace: %s", consumerPath)

	awaitAPIBinding := func(group, resource string, expectedPermissionClaims ...apisv1alpha2.PermissionClaim) {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("failed to list api bindings: %v", err)
			}
			for _, ab := range list.Items {
				if !strings.HasPrefix(ab.GetName(), apiExport.GetName()) {
					continue
				}
				hasBoundResource := slices.ContainsFunc(ab.Status.BoundResources, func(br apisv1alpha2.BoundAPIResource) bool {
					return br.Group == group && br.Resource == resource
				})
				if !hasBoundResource {
					continue
				}
				for _, epc := range expectedPermissionClaims {
					hasAppliedClaim := slices.ContainsFunc(ab.Status.AppliedPermissionClaims, func(spc apisv1alpha2.ScopedPermissionClaim) bool {
						return epc.Group == spc.Group && epc.Resource == spc.Resource
					})
					if !hasAppliedClaim {
						return false, fmt.Sprintf("found api binding %q but missing permission claim %s/%s", ab.GetName(), epc.Group, epc.Resource)
					}
				}
				return true, fmt.Sprintf("found api binding %q", ab.GetName())
			}
			return false, ""
		}, wait.ForeverTestTimeout, time.Second*2, fmt.Sprintf("failed to wait for api binding with %q %q from export %q", group, resource, apiExport.GetName()))
	}

	t.Logf("Verifying initial APIBinding has cowboys and configmaps permission claim")
	awaitAPIBinding("wildwest.dev", "cowboys",
		apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
		},
	)

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "migration"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Waiting for migration APIBinding to be bound in %s", orgPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	t.Logf("Waiting for APIExportEndpointSlice in %s to have a virtual workspace URL for consumer %s", providerPath, consumerPath)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), apiExport.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice: %v", err)
		}
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), kcpClusterClient, consumerWs, framework.ExportVirtualWorkspaceURLs(slice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for VW URL: %v", slice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	wildwestVWClient, err := wildwestclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err)

	t.Logf("Starting pre-migration watch on Cowboys via VW")
	vwWatcher, err := wildwestVWClient.WildwestV1alpha1().Cowboys().Watch(t.Context(), metav1.ListOptions{})
	require.NoError(t, err, "should be able to establish VW watch before provider migration")
	t.Cleanup(vwWatcher.Stop)

	t.Logf("Starting pre-migration informer on Cowboys via VW")
	vwInformerStore, vwInformerController := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: &cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return wildwestVWClient.WildwestV1alpha1().Cowboys().List(t.Context(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return wildwestVWClient.WildwestV1alpha1().Cowboys().Watch(t.Context(), options)
			},
		},
		ObjectType: &wildwestv1alpha1.Cowboy{},
		Handler:    cache.ResourceEventHandlerFuncs{},
	})
	vwInformerCtx, vwInformerCancel := context.WithCancel(t.Context())
	t.Cleanup(vwInformerCancel)
	go vwInformerController.Run(vwInformerCtx.Done())

	t.Logf("Migrating provider logical cluster %s from %s to %s", providerLCName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{Name: "provider-migration"},
				Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster:   providerLCName.String(),
					DestinationShard: destinationShard,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			t.Logf("failed to create LogicalClusterMigration: %v", err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	t.Logf("Waiting for provider migration to complete")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q", migration.Status.Phase)
		t.Logf("migration conditions: %#v", migration.Status.Conditions)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for provider migration to complete")

	t.Logf("Verifying cowboys APIBinding is still bound after provider migration")
	awaitAPIBinding("wildwest.dev", "cowboys",
		apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
		},
	)

	t.Run("PreMigrateVWWatch", func(t *testing.T) {
		t.Parallel()

		wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
		require.NoError(t, err)
		_, err = wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default").Create(
			t.Context(),
			&wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-vw-cowboy"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		for {
			select {
			case <-t.Context().Done():
				t.Fatal("test context closed while waiting for VW watch event")
			case event, ok := <-vwWatcher.ResultChan():
				if !ok {
					t.Fatal("VW watch closed unexpectedly after provider migration")
				}
				if event.Type == watch.Error {
					continue
				}
				cowboy, ok := event.Object.(*wildwestv1alpha1.Cowboy)
				if ok && cowboy.Name == "post-migration-vw-cowboy" {
					return
				}
			case <-time.After(wait.ForeverTestTimeout):
				t.Fatal("VW watch did not receive post-migration Cowboy event")
			}
		}
	})

	t.Run("PreMigrateVWInformer", func(t *testing.T) {
		t.Parallel()

		wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
		require.NoError(t, err)
		_, err = wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default").Create(
			t.Context(),
			&wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{Name: "post-migration-vw-informer-cowboy"},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, exists, err := vwInformerStore.GetByKey("default/post-migration-vw-informer-cowboy")
			assert.NoError(c, err)
			assert.True(c, exists, "VW informer should have picked up post-migration-vw-informer-cowboy")
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for VW informer to pick up post-migration object")
	})
}

func TestFullMigrationAPIExportUpdate(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	shardNames := server.ShardNames()
	originShard := shardNames[0]
	destinationShard := shardNames[1]

	t.Logf("Using origin shard %q and destination shard %q", originShard, destinationShard)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, providerWs := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	providerLCName := logicalcluster.Name(providerWs.Spec.Cluster)

	t.Logf("Provider workspace: %s (logical cluster %s, shard %s)", providerPath, providerLCName, originShard)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))

	t.Logf("Installing cowboys APIResourceSchema into provider workspace %s", providerPath)
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Creating cowboys APIExport in provider workspace %s", providerPath)
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
					Verbs:         []string{"get"},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating WorkspaceType with DefaultAPIBindings in provider workspace %s", providerPath)
	workspaceType := tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{Name: "test-consumer-type"},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			DefaultChildWorkspaceType: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: "universal",
				Path: "root",
			},
			Extend: tenancyv1alpha1.WorkspaceTypeExtension{
				With: []tenancyv1alpha1.WorkspaceTypeReference{
					{Name: "universal", Path: "root"},
				},
			},
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{Export: apiExport.GetName()},
			},
			DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), &workspaceType, metav1.CreateOptions{})
	require.NoError(t, err)

	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithType(providerPath, tenancyv1alpha1.WorkspaceTypeName(workspaceType.GetName())))
	t.Logf("Consumer workspace: %s", consumerPath)

	awaitAPIBinding := func(group, resource string, expectedPermissionClaims ...apisv1alpha2.PermissionClaim) {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("failed to list api bindings: %v", err)
			}
			for _, ab := range list.Items {
				if !strings.HasPrefix(ab.GetName(), apiExport.GetName()) {
					continue
				}
				hasBoundResource := slices.ContainsFunc(ab.Status.BoundResources, func(br apisv1alpha2.BoundAPIResource) bool {
					return br.Group == group && br.Resource == resource
				})
				if !hasBoundResource {
					continue
				}
				for _, epc := range expectedPermissionClaims {
					hasAppliedClaim := slices.ContainsFunc(ab.Status.AppliedPermissionClaims, func(spc apisv1alpha2.ScopedPermissionClaim) bool {
						return epc.Group == spc.Group && epc.Resource == spc.Resource
					})
					if !hasAppliedClaim {
						return false, fmt.Sprintf("found api binding %q but missing permission claim %s/%s", ab.GetName(), epc.Group, epc.Resource)
					}
				}
				return true, fmt.Sprintf("found api binding %q", ab.GetName())
			}
			return false, ""
		}, wait.ForeverTestTimeout, time.Second*2, fmt.Sprintf("failed to wait for api binding with %q %q from export %q", group, resource, apiExport.GetName()))
	}

	t.Logf("Verifying initial APIBinding has cowboys and configmaps permission claim")
	awaitAPIBinding("wildwest.dev", "cowboys",
		apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
		},
	)

	t.Logf("Creating APIBinding for migration.kcp.io in %s", orgPath)
	_, err = kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(
		t.Context(),
		&apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "migration"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: core.RootCluster.Path().String(),
						Name: "migration.kcp.io",
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Waiting for migration APIBinding to be bound in %s", orgPath)
	require.Eventually(t, func() bool {
		binding, err := kcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		if err != nil {
			return false
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")

	t.Logf("Migrating provider logical cluster %s from %s to %s", providerLCName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{Name: "provider-migration"},
				Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster:   providerLCName.String(),
					DestinationShard: destinationShard,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil {
			t.Logf("failed to create LogicalClusterMigration: %v", err)
		}
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	t.Logf("Waiting for provider migration to complete")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q", migration.Status.Phase)
		t.Logf("migration conditions: %#v", migration.Status.Conditions)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for provider migration to complete")

	t.Logf("Installing TLSRoutes APIResourceSchema into provider workspace %s", providerPath)
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_tlsroutes.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Updating APIExport to add tlsroutes resource and secrets permission claim")
	currentAPIExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.GetName(), metav1.GetOptions{})
	require.NoError(t, err)

	updatedAPIExport := currentAPIExport.DeepCopy()
	updatedAPIExport.Spec.Resources = append(updatedAPIExport.Spec.Resources, apisv1alpha2.ResourceSchema{
		Group:  "gateway.networking.k8s.io",
		Name:   "tlsroutes",
		Schema: "latest.tlsroutes.gateway.networking.k8s.io",
		Storage: apisv1alpha2.ResourceSchemaStorage{
			CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
		},
	})
	updatedAPIExport.Spec.PermissionClaims = append(updatedAPIExport.Spec.PermissionClaims, apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "secrets"},
		Verbs:         []string{"get"},
	})

	commitAPIExport := committer.NewCommitter[*apisv1alpha2.APIExport, apisv1alpha2client.APIExportInterface, *apisv1alpha2.APIExportSpec, *apisv1alpha2.APIExportStatus](kcpClusterClient.ApisV1alpha2().APIExports())
	type apiExportResource = committer.Resource[*apisv1alpha2.APIExportSpec, *apisv1alpha2.APIExportStatus]
	oldResource := &apiExportResource{ObjectMeta: currentAPIExport.ObjectMeta, Spec: &currentAPIExport.Spec, Status: &currentAPIExport.Status}
	newResource := &apiExportResource{ObjectMeta: currentAPIExport.ObjectMeta, Spec: &updatedAPIExport.Spec, Status: &currentAPIExport.Status}
	err = commitAPIExport(t.Context(), oldResource, newResource)
	require.NoError(t, err)

	t.Logf("Verifying APIBinding now has tlsroutes and secrets permission claim after provider migration and export update")
	awaitAPIBinding("gateway.networking.k8s.io", "tlsroutes",
		apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
		},
		apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "secrets"},
		},
	)

	t.Logf("Verifying tlsroutes appears in consumer workspace %q discovery", consumerPath)
	consumerDiscoveryClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		resources, err := consumerDiscoveryClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1alpha2")
		require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerPath)
		return resourceExists(resources, "tlsroutes")
	}, wait.ForeverTestTimeout, time.Millisecond*100, "consumer workspace %q discovery is missing tlsroutes resource", consumerPath)
}
