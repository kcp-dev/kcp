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

package storagerekey

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileDrainingAggregation(t *testing.T) {
	t.Parallel()
	const target = "target"
	entry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, StorageKey: target, TotalBindings: total, MigratedBindings: migrated}
	}
	staleEntry := func(shard string, total, migrated int32) migrationv1alpha1.ShardMigrationProgress {
		return migrationv1alpha1.ShardMigrationProgress{Shard: shard, StorageKey: "old", TotalBindings: total, MigratedBindings: migrated}
	}
	scenarios := []struct {
		name             string
		shards           []string
		entries          []migrationv1alpha1.ShardMigrationProgress
		expectedPhase    migrationv1alpha1.APIExportStorageRekeyPhase
		expectedTotal    int32
		expectedMigrated int32
		expectedEntries  int
		expectCleared    bool
	}{
		{
			name:             "completed when every shard reported and is fully migrated",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyCompleted,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  2,
			expectCleared:    true,
		},
		{
			name:             "not drained while a shard has not reported",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  1,
		},
		{
			name:             "not drained while a shard is mid-drain",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), entry("alpha", 3, 1)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTotal:    5,
			expectedMigrated: 3,
			expectedEntries:  2,
		},
		{
			name:             "entries of removed shards are ignored and do not block the drain",
			shards:           []string{"root"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 1, 1), entry("gone", 4, 0)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyCompleted,
			expectedTotal:    1,
			expectedMigrated: 1,
			expectedEntries:  2, // entries are migrator-owned; the controller does not prune them
			expectCleared:    true,
		},
		{
			name:             "zero bindings everywhere completes immediately",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 0, 0), entry("alpha", 0, 0)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyCompleted,
			expectedTotal:    0,
			expectedMigrated: 0,
			expectedEntries:  2,
			expectCleared:    true,
		},
		{
			// a shard that counted against another storage key reports
			// everything as migrated; that report must not satisfy the gate.
			name:             "reports against a stale storage key are ignored",
			shards:           []string{"root", "alpha"},
			entries:          []migrationv1alpha1.ShardMigrationProgress{entry("root", 2, 2), staleEntry("alpha", 3, 3)},
			expectedPhase:    migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTotal:    2,
			expectedMigrated: 2,
			expectedEntries:  2,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			export := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "today-cowboys",
					Annotations: map[string]string{
						"kcp.io/cluster": "root",
						migrationv1alpha1.ActiveRekeyAnnotationKey: "root|rekey|" + target + "|",
					},
				},
				Status: apisv1alpha2.APIExportStatus{IdentityHash: target},
			}
			var updated *migrationv1alpha1.APIExportStorageRekey
			cleared := false
			c := &Controller{
				listShardNames: func() ([]string, error) {
					return scenario.shards, nil
				},
				updateRekeyStatus: func(_ context.Context, _ logicalcluster.Path, rekey *migrationv1alpha1.APIExportStorageRekey) (*migrationv1alpha1.APIExportStorageRekey, error) {
					updated = rekey
					return rekey, nil
				},
				getAPIExport: func(_ context.Context, _ logicalcluster.Path, _ string) (*apisv1alpha2.APIExport, error) {
					return export, nil
				},
				updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					if _, has := e.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey]; has {
						t.Errorf("expected the active-rekey marker to be cleared, got %v", e.Annotations)
					}
					cleared = true
					return e, nil
				},
			}
			rekey := &migrationv1alpha1.APIExportStorageRekey{
				ObjectMeta: metav1.ObjectMeta{Name: "rekey"},
				Spec:       migrationv1alpha1.APIExportStorageRekeySpec{Export: migrationv1alpha1.ExportReference{Name: "today-cowboys"}},
				Status: migrationv1alpha1.APIExportStorageRekeyStatus{
					Phase:     migrationv1alpha1.APIExportStorageRekeyDraining,
					TargetKey: target,
					Shards:    scenario.entries,
				},
			}
			if err := c.reconcileDraining(context.Background(), "root", rekey); err != nil {
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
			drained := conditions.IsTrue(updated, migrationv1alpha1.StorageRekeyDrained)
			if wantDrained := scenario.expectedPhase == migrationv1alpha1.APIExportStorageRekeyCompleted; drained != wantDrained {
				t.Errorf("expected Drained=%v, got %v", wantDrained, drained)
			}
			if cleared != scenario.expectCleared {
				t.Errorf("expected marker cleared=%v, got %v", scenario.expectCleared, cleared)
			}
		})
	}
}

// TestReconcilePending covers validation and marking of a Pending
// re-keying, including the retry after a previous attempt already marked the
// export but failed before recording Draining.
func TestReconcilePending(t *testing.T) {
	t.Parallel()
	const identity = "identity"

	scenarios := []struct {
		name           string
		annotation     string
		spec           migrationv1alpha1.APIExportStorageRekeySpec
		expectedPhase  migrationv1alpha1.APIExportStorageRekeyPhase
		expectedTarget string
		expectMarked   bool
	}{
		{
			name:           "an empty target key resolves to the export's identity and marks the export",
			spec:           migrationv1alpha1.APIExportStorageRekeySpec{Export: migrationv1alpha1.ExportReference{Name: "today-cowboys"}},
			expectedPhase:  migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTarget: identity,
			expectMarked:   true,
		},
		{
			name:           "an explicit target key is used as is",
			spec:           migrationv1alpha1.APIExportStorageRekeySpec{Export: migrationv1alpha1.ExportReference{Name: "today-cowboys"}, TargetKey: "system-cowboys"},
			expectedPhase:  migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTarget: "system-cowboys",
			expectMarked:   true,
		},
		{
			name:           "retry after the export was already marked resumes into Draining without re-marking",
			annotation:     "root|rekey|" + identity + "|",
			spec:           migrationv1alpha1.APIExportStorageRekeySpec{Export: migrationv1alpha1.ExportReference{Name: "today-cowboys"}},
			expectedPhase:  migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTarget: identity,
		},
		{
			name:          "an active re-keying of a different object fails this one",
			annotation:    "root|other|" + identity + "|",
			spec:          migrationv1alpha1.APIExportStorageRekeySpec{Export: migrationv1alpha1.ExportReference{Name: "today-cowboys"}},
			expectedPhase: migrationv1alpha1.APIExportStorageRekeyFailed,
		},
		{
			name: "a resource the export does not serve fails validation",
			spec: migrationv1alpha1.APIExportStorageRekeySpec{
				Export:    migrationv1alpha1.ExportReference{Name: "today-cowboys"},
				Resources: []apisv1alpha2.GroupResource{{Group: "wildwest.dev", Resource: "sheriffs"}},
			},
			expectedPhase: migrationv1alpha1.APIExportStorageRekeyFailed,
		},
		{
			name: "a served resource passes validation and travels in the marker",
			spec: migrationv1alpha1.APIExportStorageRekeySpec{
				Export:    migrationv1alpha1.ExportReference{Name: "today-cowboys"},
				Resources: []apisv1alpha2.GroupResource{{Group: "wildwest.dev", Resource: "cowboys"}},
			},
			expectedPhase:  migrationv1alpha1.APIExportStorageRekeyDraining,
			expectedTarget: identity,
			expectMarked:   true,
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
					Resources: []apisv1alpha2.ResourceSchema{{
						Name: "cowboys", Group: "wildwest.dev", Schema: "today.cowboys.wildwest.dev",
						Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
					}},
				},
				Status: apisv1alpha2.APIExportStatus{IdentityHash: identity},
			}
			if scenario.annotation != "" {
				export.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey] = scenario.annotation
			}

			var updated *migrationv1alpha1.APIExportStorageRekey
			var marked *apisv1alpha2.APIExport
			c := &Controller{
				getAPIExport: func(_ context.Context, _ logicalcluster.Path, _ string) (*apisv1alpha2.APIExport, error) {
					return export, nil
				},
				updateAPIExport: func(_ context.Context, _ logicalcluster.Path, e *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
					marked = e
					return e, nil
				},
				updateRekeyStatus: func(_ context.Context, _ logicalcluster.Path, r *migrationv1alpha1.APIExportStorageRekey) (*migrationv1alpha1.APIExportStorageRekey, error) {
					updated = r
					return r, nil
				},
			}

			rekey := &migrationv1alpha1.APIExportStorageRekey{
				ObjectMeta: metav1.ObjectMeta{Name: "rekey"},
				Spec:       scenario.spec,
				Status:     migrationv1alpha1.APIExportStorageRekeyStatus{Phase: migrationv1alpha1.APIExportStorageRekeyPending},
			}

			if err := c.reconcilePending(context.Background(), logicalcluster.Name("root"), rekey); err != nil {
				t.Fatalf("reconcilePending returned error: %v", err)
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			if updated.Status.Phase != scenario.expectedPhase {
				t.Errorf("expected phase %q, got %q (conditions: %+v)", scenario.expectedPhase, updated.Status.Phase, updated.Status.Conditions)
			}
			if updated.Status.TargetKey != scenario.expectedTarget {
				t.Errorf("expected target key %q, got %q", scenario.expectedTarget, updated.Status.TargetKey)
			}
			if (marked != nil) != scenario.expectMarked {
				t.Fatalf("expected marked=%v, got %v", scenario.expectMarked, marked != nil)
			}
			if marked != nil {
				active, ok := migrationv1alpha1.ParseActiveRekey(marked.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey])
				if !ok {
					t.Fatalf("expected a parseable marker, got %q", marked.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey])
				}
				if active.Cluster != "root" || active.Name != "rekey" || active.TargetKey != scenario.expectedTarget {
					t.Errorf("unexpected marker %+v", active)
				}
				if len(active.Resources) != len(scenario.spec.Resources) {
					t.Errorf("expected %d resources in the marker, got %+v", len(scenario.spec.Resources), active.Resources)
				}
			}
		})
	}
}
