/*
Copyright 2025 The KCP Authors.

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

package authentication

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestDeleteShard(t *testing.T) {
	const shardName = "shard-1"

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(errors.New("test has ended"))

	clusterIndex := index.New(nil)
	authIndex := NewIndex(ctx, nil)

	// setup
	clusterIndex.UpsertShard("root", "https://root.io")
	clusterIndex.UpsertShard(shardName, "https://buck.io")

	// setup the root logicalcluster (has no workspace)
	clusterIndex.UpsertLogicalCluster("root", newLogicalCluster("root", "root:root"))

	// place the custom workspace type
	authIndex.UpsertWorkspaceType("root", newWorkspaceType("custom-type", "root"))

	// setup the team workspace (ws is on root shard in root workspace, cluster is on the 2nd shard)
	ws := newWorkspace("team1", "root", "logicalteamcluster")
	ws.Spec.Type = &tenancyv1alpha1.WorkspaceTypeReference{
		Name: "custom-type",
		Path: "root",
	}
	clusterIndex.UpsertWorkspace("root", ws)
	clusterIndex.UpsertLogicalCluster(shardName, newLogicalCluster("logicalteamcluster", "root:custom-type"))

	r, found := clusterIndex.Lookup(logicalcluster.NewPath("root:team1"))
	fmt.Printf("result: %+v\n", r)
	fmt.Printf("found: %v\n", found)
}

// func TestDeleteLogicalCluster(t *testing.T) {
// 	target := New(nil)

// 	// ensure deleting not existent logical cluster won't blow up
// 	target.DeleteLogicalCluster("root", newLogicalCluster("34"))

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

// 	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "34", "", true)

// 	// ensure that after deleting the logical cluster it cannot be looked up
// 	target.DeleteLogicalCluster("root", newLogicalCluster("34"))

// 	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "", "", "", false)

// 	r, found = target.Lookup(logicalcluster.NewPath("root"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "root", "", true)
// }

// func TestDeleteWorkspace(t *testing.T) {
// 	target := New(nil)

// 	// ensure deleting not existent workspace won't blow up
// 	target.DeleteWorkspace("root", newWorkspace("org", "root", "34"))

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertWorkspace("root", newWorkspace("org1", "root", "43"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("43"))

// 	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "34", "", true)

// 	target.DeleteWorkspace("root", newWorkspace("org", "root", "34"))

// 	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "", "", "", false)

// 	r, found = target.Lookup(logicalcluster.NewPath("root:org1"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "43", "", true)
// }

// func TestUpsertLogicalCluster(t *testing.T) {
// 	target := New(nil)

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertShard("amber", "https://amber.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

// 	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "34", "", true)

// 	target.UpsertLogicalCluster("amber", newLogicalCluster("34"))
// 	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "amber", "34", "", true)
// }

// // Since LookupURL uses Lookup method the following test is just a smoke tests.
// func TestLookupURL(t *testing.T) {
// 	target := New(nil)

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

// 	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
// 	if !found {
// 		t.Fatalf("expected to find a URL for %q path", "root:org")
// 	}
// 	if r.URL != "https://root.io/clusters/34" {
// 		t.Fatalf("unexpected url. returned = %v, expected = %v for %q path", r.URL, "https://root.io/clusters/34", "root:org")
// 	}

// 	r, found = target.LookupURL(logicalcluster.NewPath("root:org:rh"))
// 	if found {
// 		t.Fatalf("didn't expected to find a URL for %q path", "root:org:rh")
// 	}
// 	if len(r.URL) > 0 {
// 		t.Fatalf("unexpected url = %v returned for %q path", r.URL, "root:org:rh")
// 	}
// }

// func TestUpsertShard(t *testing.T) {
// 	target := New(nil)

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

// 	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
// 	if !found {
// 		t.Fatalf("expected to find a URL for %q path", "root:org")
// 	}
// 	if r.URL != "https://root.io/clusters/34" {
// 		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://root.io/clusters/34", "root:org")
// 	}

// 	target.UpsertShard("root", "https://new-root.io")
// 	r, found = target.LookupURL(logicalcluster.NewPath("root:org"))
// 	if !found {
// 		t.Fatalf("expected to find a URL for %q path", "root:org")
// 	}
// 	if r.URL != "https://new-root.io/clusters/34" {
// 		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://new-root.io/clusters/34", "root:org")
// 	}
// }

// func TestUpsertWorkspace(t *testing.T) {
// 	target := New(nil)

// 	target.UpsertShard("root", "https://root.io")
// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("34"))
// 	target.UpsertLogicalCluster("root", newLogicalCluster("44"))

// 	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "34", "", true)

// 	target.UpsertWorkspace("root", newWorkspace("org", "root", "44"))
// 	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
// 	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "44", "", true)
// }

// func validateLookupOutput(t *testing.T, path logicalcluster.Path, shard string, cluster logicalcluster.Name, url string, found bool, expectedShard string, expectedCluster logicalcluster.Name, expectedURL string, expectToFind bool) {
// 	t.Helper()

// 	if found != expectToFind {
// 		t.Fatalf("found != expectedToFind, for %q path", path)
// 	}
// 	if cluster != expectedCluster {
// 		t.Fatalf("unexpected logical cluster = %v, expected = %v for %q path", cluster, expectedCluster, path)
// 	}
// 	if shard != expectedShard {
// 		t.Fatalf("unexpected shard = %v, expected = %v, for %q path", shard, expectedShard, path)
// 	}
// 	if url != expectedURL {
// 		t.Fatalf("unexpected url = %v, expected = %v, for %q path", url, expectedURL, path)
// 	}
// }

func newWorkspaceType(name, cluster string) *tenancyv1alpha1.WorkspaceType {
	return &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"kcp.io/cluster": cluster}},
		Spec:       tenancyv1alpha1.WorkspaceTypeSpec{},
	}
}

func newWorkspace(name, cluster, scheduledCluster string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"kcp.io/cluster": cluster}},
		Spec:       tenancyv1alpha1.WorkspaceSpec{Cluster: scheduledCluster},
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	}
}

func newWorkspaceWithMount(name, cluster, scheduledCluster string, ref tenancyv1alpha1.ObjectReference) *tenancyv1alpha1.Workspace {
	ws := newWorkspace(name, cluster, scheduledCluster)
	ws.Spec.Mount = &tenancyv1alpha1.Mount{Reference: ref}
	return ws
}

func WithCondition(ws *tenancyv1alpha1.Workspace, condition conditionsv1alpha1.Condition) *tenancyv1alpha1.Workspace {
	ws.Status.Conditions = []conditionsv1alpha1.Condition{condition}
	return ws
}

func withPhase(ws *tenancyv1alpha1.Workspace, phase corev1alpha1.LogicalClusterPhaseType) *tenancyv1alpha1.Workspace {
	ws.Status.Phase = phase
	return ws
}

func withURL(ws *tenancyv1alpha1.Workspace, url string) *tenancyv1alpha1.Workspace {
	ws.Spec.URL = url
	return ws
}

func newLogicalCluster(cluster string, fqType string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
			Annotations: map[string]string{
				"kcp.io/cluster": cluster,
				tenancyv1alpha1.LogicalClusterTypeAnnotationKey: fqType,
			},
		},
	}
}
