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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestMigrationAbortOnWorkspaceDeletion verifies that deleting a workspace
// while its logical cluster is being migrated correctly aborts the migration
// and cleans up all data on both shards. It uses a non-existent destination
// shard to stall the migration in the Preparing phase (origin completes
// preparation but the destination never picks up).
func TestMigrationAbortOnWorkspaceDeletion(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	originShard := server.ShardNames()[0]
	// Use a non-existent shard as destination to stall the migration.
	// The origin will complete Preparing but the destination will never
	// pick up, leaving the migration stuck in the Migrating phase.
	nonExistentShard := "non-existent-shard-for-abort-test"

	t.Logf("Using origin shard %q and non-existent destination %q", originShard, nonExistentShard)

	// Create an org workspace and a child workspace pinned to the origin shard.
	// Since this blocks until the lc is ready, we can proceed directly.
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(originShard))
	lcName := logicalcluster.Name(ws.Spec.Cluster)
	wsName := ws.Name

	t.Logf("Workspace %s (logical cluster %s) created on shard %s", wsPath, lcName, originShard)

	// Create test data to verify cleanup.
	numConfigMaps := 3
	for i := range numConfigMaps {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
			t.Context(),
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("abort-test-cm-%d", i)},
				Data:       map[string]string{"index": fmt.Sprintf("%d", i)},
			},
			metav1.CreateOptions{},
		)
		require.NoError(t, err)
	}

	// Bind the migration API in the org workspace.
	bindMigrationAPI(t, kcpClusterClient, orgPath)

	// Create the migration targeting the non-existent shard.
	t.Logf("Creating LogicalClusterMigration targeting non-existent shard %q", nonExistentShard)
	var lcm *migrationv1alpha1.LogicalClusterMigration
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var createErr error
		lcm, createErr = kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
			t.Context(),
			&migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{
					Name: "abort-test-migration",
				},
				Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster:   lcName.String(),
					DestinationShard: nonExistentShard,
				},
			},
			metav1.CreateOptions{},
		)
		require.NoError(c, createErr)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration")

	// Wait for the migration to reach the Migrating phase.
	// The origin shard will complete Preparing (annotate LC, purge informers,
	// block access), then transition to Migrating. But since the destination
	// shard doesn't exist, it stays stuck in Migrating.
	t.Logf("Waiting for migration to reach Migrating phase (stalled)")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q, conditions: %v", migration.Status.Phase, conditionsSummary(migration.Status.Conditions))
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseMigrating, migration.Status.Phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "migration did not reach Migrating phase")

	// Verify the migration has the origin shard set.
	migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, originShard, migration.Status.OriginShard, "origin shard should be set in status")

	// Verify the OriginReady condition is true.
	require.True(t, hasCondition(migration.Status.Conditions, "OriginReady", corev1.ConditionTrue),
		"OriginReady condition should be True")

	// Now delete the workspace. This is the core action under test.
	t.Logf("Deleting workspace %s to trigger migration abort", wsName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Delete(t.Context(), wsName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			require.NoError(c, err)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to delete workspace")

	// Verify the migration transitions to Aborting phase.
	t.Logf("Waiting for migration to transition to Aborting phase")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q, conditions: %v", migration.Status.Phase, conditionsSummary(migration.Status.Conditions))
		phase := migration.Status.Phase
		require.True(c,
			phase == migrationv1alpha1.LogicalClusterMigrationPhaseAborting ||
				phase == migrationv1alpha1.LogicalClusterMigrationPhaseFailed,
			"expected Aborting or Failed phase, got %q", phase)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "migration did not transition to Aborting")

	// Verify the migration reaches the terminal Failed phase with Aborted condition.
	t.Logf("Waiting for migration to reach Failed phase with Aborted condition")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), lcm.Name, metav1.GetOptions{})
		require.NoError(c, err)
		t.Logf("migration phase: %q, conditions: %v", migration.Status.Phase, conditionsSummary(migration.Status.Conditions))
		require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseFailed, migration.Status.Phase)
		require.True(c, hasCondition(migration.Status.Conditions, "Aborted", corev1.ConditionTrue),
			"Aborted condition should be True")
		require.True(c, hasCondition(migration.Status.Conditions, "OriginCleaned", corev1.ConditionTrue),
			"OriginCleaned condition should be True")
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "migration did not reach Failed phase with expected conditions")

	// Verify the workspace is fully deleted.
	t.Logf("Verifying workspace is fully deleted")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(t.Context(), wsName, metav1.GetOptions{})
		require.True(c, apierrors.IsNotFound(err), "workspace should be gone, got: %v", err)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "workspace was not fully deleted")

	// Verify we cannot access the logical cluster anymore (all data gone).
	t.Logf("Verifying logical cluster data is inaccessible")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").List(t.Context(), metav1.ListOptions{})
		// The LC is gone - we expect either NotFound or Forbidden (depending on
		// whether the front-proxy still resolves the path).
		require.True(c, apierrors.IsNotFound(err) || apierrors.IsForbidden(err),
			"expected NotFound or Forbidden accessing deleted LC, got: %v", err)
	}, wait.ForeverTestTimeout, 500*time.Millisecond, "logical cluster data still accessible after abort")
}

// bindMigrationAPI creates and waits for the migration.kcp.io APIBinding to
// become bound in the given workspace.
func bindMigrationAPI(t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, wsPath logicalcluster.Path) {
	t.Helper()
	t.Logf("Creating APIBinding for migration.kcp.io in %s", wsPath)
	_, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha2().APIBindings().Create(
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

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		binding, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha2().APIBindings().Get(t.Context(), "migration", metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, apisv1alpha2.APIBindingPhaseBound, binding.Status.Phase)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "migration APIBinding never reached Bound phase")
}

// hasCondition checks if a condition with the given type and status exists.
func hasCondition(conditions conditionsv1alpha1.Conditions, condType conditionsv1alpha1.ConditionType, status corev1.ConditionStatus) bool {
	for _, c := range conditions {
		if c.Type == condType && c.Status == status {
			return true
		}
	}
	return false
}

// conditionsSummary returns a brief string summary of conditions for logging.
func conditionsSummary(conditions conditionsv1alpha1.Conditions) string {
	if len(conditions) == 0 {
		return "<none>"
	}
	result := ""
	for _, c := range conditions {
		if result != "" {
			result += ", "
		}
		result += fmt.Sprintf("%s=%s", c.Type, c.Status)
	}
	return result
}
