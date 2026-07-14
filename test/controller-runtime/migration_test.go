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

package controllerruntime

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcluster "sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
)

// TestMigrationControllerRuntimeConfigMapClient proves that a controller-runtime
// cache-backed client (cluster.New(...).GetClient(), the exact pattern a
// controller-runtime / multicluster-runtime reconciler uses) keeps working while
// the logical cluster it watches is migrated to another shard.
//
// A controller-runtime client reads through an informer cache driven by a
// client-go reflector. Unlike a raw watch.Interface it re-Lists and re-Watches
// on its own when the migration severs its stream, so it must keep serving reads
// across the migration - both for objects that existed before the migration and
// for objects created afterwards. The 410 Gone behaviour of the migrating filter
// is what makes the reflector's post-migration relist resolve cleanly against the
// destination shard instead of hanging on the origin shard's resource version.
func TestMigrationControllerRuntimeConfigMapClient(t *testing.T) {
	t.Parallel()

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

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	lcName := logicalcluster.Name(ws.Spec.Cluster)

	t.Logf("Workspace %s (logical cluster %s) created on shard %s", wsPath, lcName, originShard)

	// Build a controller-runtime cache-backed cluster/client scoped to the
	// workspace, exactly like a controller-runtime / multicluster-runtime
	// reconciler does (cl := cluster.New(cfg); client := cl.GetClient()). The
	// rest.Config host is scoped to the logical cluster so every request is
	// routed to whichever shard currently hosts it; the front proxy re-routes
	// to the destination shard after the migration.
	ctrllog.SetLogger(logr.Discard())

	wsCfg := rest.CopyConfig(cfg)
	wsCfg.Host += wsPath.RequestPath()

	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))

	ctrlCluster, err := ctrlcluster.New(wsCfg, func(o *ctrlcluster.Options) {
		o.Scheme = scheme
	})
	require.NoError(t, err, "should be able to build a controller-runtime cluster for the workspace")

	cacheCtx, cacheCancel := context.WithCancel(t.Context())
	t.Cleanup(cacheCancel)
	go func() {
		// Start blocks until cacheCtx is cancelled; errors here are only
		// meaningful once the cache is running, which WaitForCacheSync covers.
		_ = ctrlCluster.Start(cacheCtx)
	}()

	crClient := ctrlCluster.GetClient()

	// Force the ConfigMap informer to be created and synced *before* the
	// migration so that its watch stream actually spans the migration (that is
	// the whole point - proving the reflector reconnects rather than a fresh
	// post-migration client trivially working).
	t.Logf("Starting pre-migration controller-runtime cache on ConfigMaps")
	_, err = ctrlCluster.GetCache().GetInformer(cacheCtx, &corev1.ConfigMap{})
	require.NoError(t, err, "should be able to start the ConfigMap informer before migration")
	require.True(t, ctrlCluster.GetCache().WaitForCacheSync(cacheCtx), "controller-runtime cache should sync before migration")

	t.Logf("Creating pre-migration ConfigMap in %s", wsPath)
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
		t.Context(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "pre-migration-cm"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Logf("Verifying controller-runtime cached client observes the pre-migration ConfigMap")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := &corev1.ConfigMap{}
		assert.NoError(c, crClient.Get(cacheCtx, ctrlclient.ObjectKey{Namespace: "default", Name: "pre-migration-cm"}, got))
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "controller-runtime cache should observe the pre-migration ConfigMap")

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

	t.Logf("Migrating logical cluster %s from %s to %s", lcName, originShard, destinationShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.Eventually(t, func() bool {
		var err error
		lcm, err = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{Name: "cr-migration"},
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
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration to complete")

	t.Logf("Creating post-migration ConfigMap in %s", wsPath)
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
		t.Context(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "post-migration-cm"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	// The controller-runtime cache's watch was severed by the migration. It must
	// reconnect on its own (relist against the destination shard, which the 410
	// Gone migrating-filter behaviour makes resolve cleanly) and pick up the
	// object created after the migration.
	t.Logf("Verifying controller-runtime cached client picks up the post-migration ConfigMap after reconnecting")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := &corev1.ConfigMap{}
		assert.NoError(c, crClient.Get(cacheCtx, ctrlclient.ObjectKey{Namespace: "default", Name: "post-migration-cm"}, got))
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "controller-runtime cache should observe the post-migration ConfigMap after reconnecting")

	// The object that existed before the migration must remain readable through
	// the same cache after the migration (data continuity, not a fresh client).
	t.Logf("Verifying the pre-migration ConfigMap is still readable via the controller-runtime cache after migration")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		got := &corev1.ConfigMap{}
		assert.NoError(c, crClient.Get(cacheCtx, ctrlclient.ObjectKey{Namespace: "default", Name: "pre-migration-cm"}, got))
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "controller-runtime cache should still serve the pre-migration ConfigMap after migration")
}
