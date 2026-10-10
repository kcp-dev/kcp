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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/informer"
)

// An empty factory is enough to exercise the reconciler's PurgeCluster calls.
func newEmptyDDSIF() *informer.DiscoveringDynamicSharedInformerFactory {
	return &informer.DiscoveringDynamicSharedInformerFactory{
		GenericDiscoveringDynamicSharedInformerFactory: &informer.GenericDiscoveringDynamicSharedInformerFactory[kcpcache.ScopeableSharedIndexInformer, kcpcache.GenericClusterLister, kcpinformers.GenericClusterInformer]{},
	}
}

func TestPreparingDisconnectsWildcardWatches(t *testing.T) {
	t.Parallel()
	lcName := logicalcluster.Name("consumer")
	migration := &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Annotations: map[string]string{logicalcluster.AnnotationKey: "org"}},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: lcName.String()},
		Status:     migrationv1alpha1.LogicalClusterMigrationStatus{OriginShard: "origin"},
	}
	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(&corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{
		Name:        corev1alpha1.LogicalClusterName,
		Annotations: map[string]string{logicalcluster.AnnotationKey: lcName.String(), MigratingAnnotationKey: "org:move"},
	}}))
	var cancelled, deleted []logicalcluster.Path
	c := &Controller{
		logicalClusterLister:     corev1alpha1listers.NewLogicalClusterClusterLister(indexer),
		migratingLogicalClusters: NewMigratingLogicalClusters(),
		ddsif:                    newEmptyDDSIF(),
		cancelLogicalClusterConnections: func(path logicalcluster.Path, reason error) {
			cancelled = append(cancelled, path)
		},
		deleteLogicalClusterContext: func(path logicalcluster.Path, reason error) {
			deleted = append(deleted, path)
		},
	}
	_, err := c.reconcilePreparing(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, []logicalcluster.Path{lcName.Path()}, cancelled, "only the migrating workspace stays blocked")
	require.Equal(t, []logicalcluster.Path{logicalcluster.Wildcard}, deleted, "wildcard watches must disconnect without blocking new requests")
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseMigrating, migration.Status.Phase)
}

func TestMigratingDisconnectsWildcardWatchesBeforeCopy(t *testing.T) {
	t.Parallel()
	migration := &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Annotations: map[string]string{logicalcluster.AnnotationKey: "org"}},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: "consumer"},
	}
	var deleted []logicalcluster.Path
	copyError := errors.New("copy failed")
	c := &Controller{
		migratingLogicalClusters: NewMigratingLogicalClusters(),
		ddsif:                    newEmptyDDSIF(),
		deleteLogicalClusterContext: func(path logicalcluster.Path, reason error) {
			deleted = append(deleted, path)
		},
		copyPageFromOrigin: func(context.Context, logicalcluster.Name, string, string) (int64, string, error) {
			require.Equal(t, []logicalcluster.Path{logicalcluster.Wildcard}, deleted, "disconnect before writing any copied data")
			return 0, "", copyError
		},
	}
	_, err := c.reconcileMigrating(t.Context(), migration)
	require.ErrorIs(t, err, copyError)
	// Retrying a failed page must not repeatedly disconnect unrelated watchers.
	_, err = c.reconcileMigrating(t.Context(), migration)
	require.ErrorIs(t, err, copyError)
	require.Len(t, deleted, 1)
}

func TestApplyDumpPageResult_requeuesWhileContinueTokenPresent(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}

	requeue := applyDumpPageResult(migration, 100, "some-continue-token")

	require.True(t, requeue)
	require.Equal(t, int64(100), migration.Status.EntriesCopied)
	require.Equal(t, "some-continue-token", migration.Status.DumpContinue)
	require.Empty(t, migration.Status.Phase, "phase must not transition while a page remains")
	require.Nil(t, conditions.Get(migration, migrationv1alpha1.LCMigrationDataCopied))
}

func TestApplyDumpPageResult_transitionsToOriginCleanupWhenDone(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}
	migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseMigrating

	requeue := applyDumpPageResult(migration, 42, "")

	require.False(t, requeue)
	require.Equal(t, int64(42), migration.Status.EntriesCopied)
	require.Empty(t, migration.Status.DumpContinue)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup, migration.Status.Phase)

	cond := conditions.Get(migration, migrationv1alpha1.LCMigrationDataCopied)
	require.NotNil(t, cond)
	require.Equal(t, "True", string(cond.Status))
}

func TestApplyDumpPageResult_accumulatesEntriesCopiedAcrossMultiplePages(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}

	require.True(t, applyDumpPageResult(migration, 10, "token-1"))
	require.True(t, applyDumpPageResult(migration, 15, "token-2"))
	require.False(t, applyDumpPageResult(migration, 5, ""))

	require.Equal(t, int64(30), migration.Status.EntriesCopied)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup, migration.Status.Phase)
}

// TestApplyDumpPageResult_clearsStaleCopyFailedConditionOnLaterSuccess covers
// a bug where a transient failure on one page (which marks DataCopied
// False/CopyFailed) would leave that condition stuck at False forever,
// even after a later page copy succeeded, because only the final page used
// to touch the condition.
func TestApplyDumpPageResult_clearsStaleCopyFailedConditionOnLaterSuccess(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}
	conditions.MarkFalse(
		migration,
		migrationv1alpha1.LCMigrationDataCopied,
		"CopyFailed",
		conditionsv1alpha1.ConditionSeverityError,
		"some transient error",
	)

	requeue := applyDumpPageResult(migration, 10, "more-to-come")

	require.True(t, requeue)
	require.Nil(t, conditions.Get(migration, migrationv1alpha1.LCMigrationDataCopied), "a successful page must clear the stale CopyFailed condition even if the copy isn't done yet")
}

// TestApplyDumpPageResult_resumesAfterSimulatedRestart mimics a destination
// shard restart between two pages: only migration.Status survives (as it
// would across a controller process restart, since it's persisted to
// etcd), and a fresh copy of the object picks up from status.dumpContinue.
func TestApplyDumpPageResult_resumesAfterSimulatedRestart(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}
	requeue := applyDumpPageResult(migration, 20, "resume-here")
	require.True(t, requeue)
	require.Equal(t, "resume-here", migration.Status.DumpContinue)

	// Simulate a shard restart: only the persisted status survives, a
	// brand new in-memory object is reconstructed from it.
	restarted := &migrationv1alpha1.LogicalClusterMigration{Status: migration.Status}
	require.Equal(t, "resume-here", restarted.Status.DumpContinue, "resume token must survive the simulated restart")

	requeue = applyDumpPageResult(restarted, 30, "")
	require.False(t, requeue)
	require.Equal(t, int64(50), restarted.Status.EntriesCopied, "entries from before and after the restart must both be counted")
	require.Empty(t, restarted.Status.DumpContinue)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup, restarted.Status.Phase)
}

func TestAddFinalizer(t *testing.T) {
	t.Parallel()

	migration := &migrationv1alpha1.LogicalClusterMigration{}
	migration.Finalizers = []string{"other"}

	require.True(t, addFinalizer(migration))
	require.Equal(t, []string{"other", MigrationFinalizer}, migration.Finalizers)

	require.False(t, addFinalizer(migration), "adding the finalizer twice must be a noop")
	require.Equal(t, []string{"other", MigrationFinalizer}, migration.Finalizers)
}

func TestReconcile_removesFinalizerInTerminalPhases(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		shard              string
		phase              migrationv1alpha1.LogicalClusterMigrationPhaseType
		expectedFinalizers []string
	}{
		"completed on destination removes finalizer": {
			shard:              "destination",
			phase:              migrationv1alpha1.LogicalClusterMigrationPhaseCompleted,
			expectedFinalizers: []string{"other"},
		},
		"completed on origin keeps finalizer": {
			shard:              "origin",
			phase:              migrationv1alpha1.LogicalClusterMigrationPhaseCompleted,
			expectedFinalizers: []string{"other", MigrationFinalizer},
		},
		"failed on origin removes finalizer": {
			shard:              "origin",
			phase:              migrationv1alpha1.LogicalClusterMigrationPhaseFailed,
			expectedFinalizers: []string{"other"},
		},
		"failed on destination removes finalizer": {
			shard:              "destination",
			phase:              migrationv1alpha1.LogicalClusterMigrationPhaseFailed,
			expectedFinalizers: []string{"other"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			migration := &migrationv1alpha1.LogicalClusterMigration{}
			migration.Finalizers = []string{"other", MigrationFinalizer}
			migration.Spec.DestinationShard = "destination"
			migration.Status.OriginShard = "origin"
			migration.Status.Phase = tc.phase

			c := &Controller{shardName: tc.shard}
			requeue, err := c.reconcile(context.Background(), migration)

			require.NoError(t, err)
			require.False(t, requeue)
			require.Equal(t, tc.expectedFinalizers, migration.Finalizers)
		})
	}
}
