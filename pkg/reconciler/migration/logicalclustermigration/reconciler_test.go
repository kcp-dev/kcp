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
	"testing"

	"github.com/stretchr/testify/require"

	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

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
