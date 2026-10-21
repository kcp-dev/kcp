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

package storagemigrator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
)

// bindingOn returns a binding whose single bound resource serves from the
// given storage key with the given pending keys.
func bindingOn(key string, pending ...string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		Status: apisv1alpha2.APIBindingStatus{
			BoundResources: []apisv1alpha2.BoundAPIResource{{
				Group:    "wildwest.dev",
				Resource: "cowboys",
				Schema:   apisv1alpha2.BoundAPIResourceSchema{IdentityHash: "identity"},
				Storage:  apisv1alpha2.BoundAPIResourceStorage{Key: key, PendingKeys: pending},
			}},
		},
	}
}

func TestReportProgress(t *testing.T) {
	t.Parallel()

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:               "provider",
				migrationv1alpha1.ActiveRekeyAnnotationKey: "ops|rekey-1|new|",
			},
		},
		// the target comes from the marker, never from the export's identity.
		Status: apisv1alpha2.APIExportStatus{IdentityHash: "identity"},
	}

	var appliedCluster logicalcluster.Path
	var appliedName string
	var applied []shardCounts
	// reported is what the rotation object currently holds for this shard.
	var reported *migrationv1alpha1.ShardMigrationProgress
	c := &Controller{
		shardName: "alpha",
		getReportedProgress: func(context.Context, logicalcluster.Name, string) (*migrationv1alpha1.ShardMigrationProgress, error) {
			return reported, nil
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return export, nil
		},
		listBindingsForExport: func(*apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{
				bindingOn("new"),        // drained
				bindingOn("new", "old"), // flipped, old prefix not deleted yet
				bindingOn("old", "new"), // not flipped yet
			}, nil
		},
		applyShardProgress: func(_ context.Context, cluster logicalcluster.Path, name string, counts shardCounts) error {
			appliedCluster, appliedName = cluster, name
			applied = append(applied, counts)
			reported = &migrationv1alpha1.ShardMigrationProgress{Shard: "alpha", StorageKey: counts.storageKey, TotalBindings: counts.total, MigratedBindings: counts.migrated}
			return nil
		},
	}

	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected one apply, got %d", len(applied))
	}
	if appliedCluster.String() != "ops" || appliedName != "rekey-1" {
		t.Errorf("report went to %s|%s, expected ops|rekey-1", appliedCluster, appliedName)
	}
	if applied[0] != (shardCounts{storageKey: "new", total: 3, migrated: 1}) {
		t.Errorf("expected counts 1/3 against storage key \"new\", got %d/%d against %q", applied[0].migrated, applied[0].total, applied[0].storageKey)
	}

	// unchanged counts must not produce a second write.
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected apply to be skipped for unchanged counts, got %d applies", len(applied))
	}

	// a lost or stale entry on the re-keying object is re-applied, even
	// though this shard already applied these counts once.
	reported = &migrationv1alpha1.ShardMigrationProgress{Shard: "alpha", StorageKey: "new", TotalBindings: 3, MigratedBindings: 0}
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 2 {
		t.Fatalf("expected the stale entry to be re-applied, got %d applies", len(applied))
	}
	reported = nil
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 3 {
		t.Fatalf("expected a missing entry to be re-applied, got %d applies", len(applied))
	}

	// no active re-keying: nothing to report.
	delete(export.Annotations, migrationv1alpha1.ActiveRekeyAnnotationKey)
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 3 {
		t.Fatalf("expected no report without an active re-keying, got %d applies", len(applied))
	}
}
