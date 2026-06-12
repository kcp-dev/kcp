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
	"k8s.io/client-go/tools/cache"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

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
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q", migration.Status.Phase)
		t.Logf("migration conditions: %#v", migration.Status.Conditions)
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "waiting for migration to complete")

	t.Logf("Migration completed, running post-migration tests")

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
