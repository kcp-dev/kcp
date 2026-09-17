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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileMigratingAggregation(t *testing.T) {
	t.Parallel()
	const newHash = "new"
	entry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, IdentityHash: newHash, TotalBindings: total, MigratedBindings: migrated}
	}
	staleEntry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, IdentityHash: "old", TotalBindings: total, MigratedBindings: migrated}
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
		{
			// a shard that counted against the pre-rotation identity (stale
			// replicated export status) reports everything as migrated; that
			// report must not satisfy the gate.
			name:             "reports against a stale identity are ignored",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), staleEntry("alpha", 3, 3)},
			expectedPhase:    migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedTotal:    2,
			expectedMigrated: 2,
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
					Phase:           migrationv1alpha1.APIExportIdentityRotationMigrating,
					NewIdentityHash: newHash,
					Shards:          scenario.entries,
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

// TestReconcilePendingRetryAfterFlip covers the retry of a Pending rotation
// after a previous attempt already flipped the export but failed before
// recording Migrating on the rotation (e.g. a conflict on the export status
// write, racing the apiexport identity reconciler that re-derives
// status.identityHash from the new secretRef). The export's current identity
// then already equals the new hash; without the active-rotation annotation
// recovery this re-validation used to terminally fail the rotation with the
// fresh-secret error.
func TestReconcilePendingRetryAfterFlip(t *testing.T) {
	t.Parallel()
	const (
		oldHash = "oldhash"
		newHash = "newhash"
	)

	scenarios := []struct {
		name            string
		annotation      string
		exportIdentity  string
		expectedPhase   migrationv1alpha1.APIExportIdentityRotationPhase
		expectedOldHash string
	}{
		{
			name:            "retry after flip resumes into Migrating with the annotation's old hash",
			annotation:      "root|rot|" + newHash + "|" + oldHash,
			exportIdentity:  newHash,
			expectedPhase:   migrationv1alpha1.APIExportIdentityRotationMigrating,
			expectedOldHash: oldHash,
		},
		{
			name:           "a genuinely reused secret without an active rotation still fails",
			annotation:     "",
			exportIdentity: newHash,
			expectedPhase:  migrationv1alpha1.APIExportIdentityRotationFailed,
		},
		{
			name:           "an active rotation of a different rotation object does not mask the reuse error",
			annotation:     "root|other-rot|" + newHash + "|" + oldHash,
			exportIdentity: newHash,
			expectedPhase:  migrationv1alpha1.APIExportIdentityRotationFailed,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			export := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "today-cowboys",
					Annotations: map[string]string{"kcp.io/cluster": "root"},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Identity: &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "default", Name: "new-secret"}},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash:        scenario.exportIdentity,
					IdentityAliasHashes: []string{oldHash},
				},
			}
			if scenario.annotation != "" {
				export.Annotations[migrationv1alpha1.ActiveRotationAnnotationKey] = scenario.annotation
			}

			var updated *migrationv1alpha1.APIExportIdentityRotation
			c := &Controller{
				getAPIExport: func(_ logicalcluster.Path, _ string) (*apisv1alpha2.APIExport, error) {
					return export, nil
				},
				getSecretHash: func(_ context.Context, _ logicalcluster.Path, _, _ string) (string, error) {
					return newHash, nil
				},
				updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					return e, nil
				},
				updateAPIExportStatus: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					return e, nil
				},
				updateRotationStatus: func(_ context.Context, _ logicalcluster.Path, r *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
					updated = r
					return r, nil
				},
			}

			rotation := &migrationv1alpha1.APIExportIdentityRotation{
				ObjectMeta: metav1.ObjectMeta{Name: "rot"},
				Spec: migrationv1alpha1.APIExportIdentityRotationSpec{
					Export:      migrationv1alpha1.ExportReference{Name: "today-cowboys"},
					NewIdentity: apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "default", Name: "new-secret"}},
				},
				Status: migrationv1alpha1.APIExportIdentityRotationStatus{
					Phase: migrationv1alpha1.APIExportIdentityRotationPending,
				},
			}

			if err := c.reconcilePending(context.Background(), logicalcluster.Name("root"), rotation); err != nil {
				t.Fatalf("reconcilePending returned error: %v", err)
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			if updated.Status.Phase != scenario.expectedPhase {
				t.Errorf("expected phase %q, got %q (conditions: %+v)", scenario.expectedPhase, updated.Status.Phase, updated.Status.Conditions)
			}
			if scenario.expectedPhase == migrationv1alpha1.APIExportIdentityRotationMigrating {
				if updated.Status.OldIdentityHash != scenario.expectedOldHash {
					t.Errorf("expected old identity hash %q, got %q", scenario.expectedOldHash, updated.Status.OldIdentityHash)
				}
				if updated.Status.NewIdentityHash != newHash {
					t.Errorf("expected new identity hash %q, got %q", newHash, updated.Status.NewIdentityHash)
				}
			}
		})
	}
}
