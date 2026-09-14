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

package identitymigrator

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
)

func bindingOn(schemaHash string, dataHashes ...string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		Status: apisv1alpha2.APIBindingStatus{
			BoundResources: []apisv1alpha2.BoundAPIResource{{
				Schema:         apisv1alpha2.BoundAPIResourceSchema{IdentityHash: schemaHash},
				IdentityHashes: dataHashes,
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
				logicalcluster.AnnotationKey:                  "provider",
				migrationv1alpha1.ActiveRotationAnnotationKey: "ops|rot-1|new|old",
			},
		},
		// the replicated export status may still carry the old identity when
		// the annotation shows up; the target must come from the annotation.
		Status: apisv1alpha2.APIExportStatus{IdentityHash: "old"},
	}

	var appliedCluster logicalcluster.Path
	var appliedName string
	var applied []shardCounts
	c := &Controller{
		shardName:    "alpha",
		lastReported: map[string]shardCounts{},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return export, nil
		},
		listBindingsForExport: func(*apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
			return []*apisv1alpha2.APIBinding{
				bindingOn("new", "new"),        // drained
				bindingOn("new", "new", "old"), // still draining
				bindingOn("old", "old"),        // not flipped yet
			}, nil
		},
		applyShardProgress: func(_ context.Context, cluster logicalcluster.Path, name string, counts shardCounts) error {
			appliedCluster, appliedName = cluster, name
			applied = append(applied, counts)
			return nil
		},
	}

	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected one apply, got %d", len(applied))
	}
	if appliedCluster.String() != "ops" || appliedName != "rot-1" {
		t.Errorf("report went to %s|%s, expected ops|rot-1", appliedCluster, appliedName)
	}
	if applied[0] != (shardCounts{identityHash: "new", total: 3, migrated: 1}) {
		t.Errorf("expected counts 1/3 against identity \"new\", got %d/%d against %q", applied[0].migrated, applied[0].total, applied[0].identityHash)
	}

	// unchanged counts must not produce a second write.
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected apply to be skipped for unchanged counts, got %d applies", len(applied))
	}

	// no active rotation: nothing to report.
	delete(export.Annotations, migrationv1alpha1.ActiveRotationAnnotationKey)
	if err := c.reportProgress(context.Background(), "provider|cowboys"); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected no report without an active rotation, got %d applies", len(applied))
	}
}
