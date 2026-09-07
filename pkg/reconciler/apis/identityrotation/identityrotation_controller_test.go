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

package identityrotation

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileMigratingAggregation(t *testing.T) {
	t.Parallel()
	entry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, TotalBindings: total, MigratedBindings: migrated}
	}
	scenarios := []struct {
		name             string
		shards           []string
		entries          []migrationv1alpha1.ShardMigrationProgress
		expectedPhase    migrationv1alpha1.APIExportIdentityRotationPhase
		expectedTotal    int32
		expectedMigrated int32
		expectedEntries  int
	}{
		{
			name:             "drained when every shard reported and is fully migrated",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  2,
		},
		{
			name:             "not drained while a shard has not reported",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  1,
		},
		{
			name:             "not drained while a shard is mid-drain",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 3, 1)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    5,
			expectedMigrated: 3,
			expectedEntries:  2,
		},
		{
			name:             "entries of removed shards are ignored and do not block the drain",
			shards:           []string{"root"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 1, 1), entry("gone", 4, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    1,
			expectedMigrated: 1,
			expectedEntries:  2, // entries are migrator-owned; the controller does not prune them
		},
		{
			name:             "zero bindings everywhere drains immediately",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 0, 0), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationAliasActive,
			expectedTotal:    0,
			expectedMigrated: 0,
			expectedEntries:  2,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			var updated *migrationv1alpha1.APIExportIdentityRotation
			c := &Controller{
				listShardNames: func() ([]string, error) {
					return scenario.shards, nil
				},
				updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
					updated = rotation
					return rotation, nil
				},
			}
			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				ObjectMeta: metav1.ObjectMeta{Name: "rot"},
				Status: migrationv1alpha1.APIExportIdentityRotationStatus{
					Phase:  migrationv1alpha1.APIExportIdentityRotationMigrating,
					Shards: scenario.entries,
				},
			}
			if err := c.reconcileMigrating(context.Background(), "root", rotation); err != nil {
				t.Fatal(err)
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			if updated.Status.Phase != scenario.expectedPhase {
				t.Errorf("expected phase %q, got %q", scenario.expectedPhase, updated.Status.Phase)
			}
			if updated.Status.TotalBindings != scenario.expectedTotal || updated.Status.MigratedBindings != scenario.expectedMigrated {
				t.Errorf("expected %d/%d bindings, got %d/%d", scenario.expectedMigrated, scenario.expectedTotal, updated.Status.MigratedBindings, updated.Status.TotalBindings)
			}
			if len(updated.Status.Shards) != scenario.expectedEntries {
				t.Errorf("expected %d shard entries, got %d", scenario.expectedEntries, len(updated.Status.Shards))
			}
			drained := conditions.IsTrue(updated, migrationv1alpha1.IdentityRotationDrained)
			if wantDrained := scenario.expectedPhase == migrationv1alpha1.APIExportIdentityRotationAliasActive; drained != wantDrained {
				t.Errorf("expected Drained=%v, got %v", wantDrained, drained)
			}
		})
	}
}
