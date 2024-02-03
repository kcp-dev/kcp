/*
Copyright 2023 The KCP Authors.

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

package index

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

type shardStub struct {
	name string
	url  string
}

func TestLookup(t *testing.T) {
	scenarios := []struct {
		name                           string
		targetPath                     logicalcluster.Path
		initialShardsToUpsert          []shardStub
		initialWorkspacesToUpsert      map[string][]*tenancyv1alpha1.Workspace
		initialLogicalClustersToUpsert map[string][]*corev1alpha1.LogicalCluster

		expectedShard   string
		expectedCluster logicalcluster.Name
		expectFound     bool
	}{
		{
			name: "an empty indexer is usable",
		},
		{
			name:       "empty path",
			targetPath: logicalcluster.NewPath(""),
		},
		{
			name: "a ns must be scheduled to be considered",
			initialShardsToUpsert: []shardStub{{
				name: "root",
				url:  "https://root.io",
			}},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {func() *tenancyv1alpha1.Workspace {
					ws := newWorkspace("org", "root", "34")
					ws.Status.Phase = corev1alpha1.LogicalClusterPhaseScheduling
					return ws
				}()},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root": {newLogicalCluster("root"), newLogicalCluster("34")},
			},
			targetPath: logicalcluster.NewPath("root:org"),
		},
		{
			name: "single shard: a logical cluster for root:org workspace is found",
			initialShardsToUpsert: []shardStub{{
				name: "root",
				url:  "https://root.io",
			}},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "34"), newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root": {newLogicalCluster("root"), newLogicalCluster("34"), newLogicalCluster("2v")},
			},
			targetPath:      logicalcluster.NewPath("root:org"),
			expectFound:     true,
			expectedCluster: "34",
			expectedShard:   "root",
		},
		{
			name: "multiple shards: a logical cluster for root:org workspace is found",
			initialShardsToUpsert: []shardStub{
				{
					name: "root",
					url:  "https://root.io",
				},
				{
					name: "amber",
					url:  "https://amber.io",
				},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "34"), newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root":  {newLogicalCluster("root")},
				"amber": {newLogicalCluster("34"), newLogicalCluster("2v")},
			},
			targetPath:      logicalcluster.NewPath("root:org"),
			expectFound:     true,
			expectedCluster: "34",
			expectedShard:   "amber",
		},
		{
			name: "multiple shards: a logical cluster for root:org:rh workspace is found",
			initialShardsToUpsert: []shardStub{
				{
					name: "root",
					url:  "https://root.io",
				},
				{
					name: "amber",
					url:  "https://amber.io",
				},
				{
					name: "silver",
					url:  "https://silver.io",
				},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root":  {newWorkspace("org", "root", "34")},
				"amber": {newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root":   {newLogicalCluster("root")},
				"amber":  {newLogicalCluster("34")},
				"silver": {newLogicalCluster("2v")},
			},
			targetPath:      logicalcluster.NewPath("root:org:rh"),
			expectFound:     true,
			expectedCluster: "2v",
			expectedShard:   "silver",
		},
		{
			name: "multiple shards: a logical cluster for 34:rh workspace is found",
			initialShardsToUpsert: []shardStub{
				{
					name: "root",
					url:  "https://root.io",
				},
				{
					name: "amber",
					url:  "https://amber.io",
				},
				{
					name: "silver",
					url:  "https://silver.io",
				},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root":  {newWorkspace("org", "root", "34")},
				"amber": {newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root":   {newLogicalCluster("root")},
				"amber":  {newLogicalCluster("34")},
				"silver": {newLogicalCluster("2v")},
			},
			targetPath:      logicalcluster.NewPath("34:rh"),
			expectFound:     true,
			expectedCluster: "2v",
			expectedShard:   "silver",
		},
		{
			name: "multiple shards: a logical cluster for 666:rh workspace is NOT found",
			initialShardsToUpsert: []shardStub{
				{
					name: "root",
					url:  "https://root.io",
				},
				{
					name: "amber",
					url:  "https://amber.io",
				},
				{
					name: "silver",
					url:  "https://silver.io",
				},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root":  {newWorkspace("org", "root", "34")},
				"amber": {newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root":   {newLogicalCluster("root")},
				"amber":  {newLogicalCluster("34")},
				"silver": {newLogicalCluster("2v")},
			},
			targetPath:  logicalcluster.NewPath("666:rh"),
			expectFound: false,
		},
		{
			name: "multiple shards: a logical cluster for root:34:rh workspace is NOT found",
			initialShardsToUpsert: []shardStub{
				{
					name: "root",
					url:  "https://root.io",
				},
				{
					name: "amber",
					url:  "https://amber.io",
				},
				{
					name: "silver",
					url:  "https://silver.io",
				},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root":  {newWorkspace("org", "root", "34")},
				"amber": {newWorkspace("rh", "34", "2v")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root":   {newLogicalCluster("root")},
				"amber":  {newLogicalCluster("34")},
				"silver": {newLogicalCluster("2v")},
			},
			targetPath:  logicalcluster.NewPath("root:34:rh"),
			expectFound: false,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			target := New(nil)
			for _, shard := range scenario.initialShardsToUpsert {
				target.UpsertShard(shard.name, shard.url)
			}
			for shardName, workspaces := range scenario.initialWorkspacesToUpsert {
				for _, ws := range workspaces {
					target.UpsertWorkspace(shardName, ws)
				}
			}
			for shardName, logicalclusters := range scenario.initialLogicalClustersToUpsert {
				for _, lc := range logicalclusters {
					target.UpsertLogicalCluster(shardName, lc)
				}
			}

			r, found := target.Lookup(scenario.targetPath)
			if scenario.expectFound && !found {
				t.Fatalf("expected to lookup the path = %v", scenario.targetPath)
			}
			if !scenario.expectFound && r.Found {
				t.Errorf("didn't expect to lookup the path = %v", scenario.targetPath)
			}
			if !scenario.expectFound {
				return
			}
			if r.Shard != scenario.expectedShard {
				t.Errorf("unexpected shard = %v, for path = %v, expected = %v", r.Shard, scenario.targetPath, scenario.expectedShard)
			}
			if r.Cluster != scenario.expectedCluster {
				t.Errorf("unexpected cluster = %v, for path = %v, expected = %v", r.Cluster, scenario.targetPath, scenario.expectedCluster)
			}
		})
	}
}

func TestDeleteShard(t *testing.T) {
	target := New(nil)

	// ensure deleting not existing shard won't blow up
	target.DeleteShard("root")

	target.UpsertShard("root", "https://root.io")
	target.UpsertShard("amber", "https://amber.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertWorkspace("root", newWorkspace("org1", "root", "43"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))
	target.UpsertLogicalCluster("amber", newLogicalCluster("43"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "amber", "43", true)

	// delete the shard and ensure we cannot look up a path on it
	target.DeleteShard("amber")
	r, found = target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "34", true)
}

func TestDeleteLogicalCluster(t *testing.T) {
	target := New(nil)

	// ensure deleting not existent logical cluster won't blow up
	target.DeleteLogicalCluster("root", newLogicalCluster("34"))

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "34", true)

	// ensure that after deleting the logical cluster it cannot be looked up
	target.DeleteLogicalCluster("root", newLogicalCluster("34"))

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "root", true)
}

func TestDeleteWorkspace(t *testing.T) {
	target := New(nil)

	// ensure deleting not existent workspace won't blow up
	target.DeleteWorkspace("root", newWorkspace("org", "root", "34"))

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertWorkspace("root", newWorkspace("org1", "root", "43"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("43"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "34", true)

	target.DeleteWorkspace("root", newWorkspace("org", "root", "34"))

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "43", true)
}

func TestUpsertLogicalCluster(t *testing.T) {
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertShard("amber", "https://amber.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "34", true)

	target.UpsertLogicalCluster("amber", newLogicalCluster("34"))
	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "amber", "34", true)
}

// Since LookupURL uses Lookup method the following test is just a smoke tests.
func TestLookupURL(t *testing.T) {
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://root.io/clusters/34" {
		t.Fatalf("unexpected url. returned = %v, expected = %v for %q path", r.URL, "https://root.io/clusters/34", "root:org")
	}

	r, found = target.LookupURL(logicalcluster.NewPath("root:org:rh"))
	if found {
		t.Fatalf("didn't expected to find a URL for %q path", "root:org:rh")
	}
	if len(r.URL) > 0 {
		t.Fatalf("unexpected url = %v returned for %q path", r.URL, "root:org:rh")
	}
}

func TestUpsertShard(t *testing.T) {
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))

	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://root.io/clusters/34" {
		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://root.io/clusters/34", "root:org")
	}

	target.UpsertShard("root", "https://new-root.io")
	r, found = target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://new-root.io/clusters/34" {
		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://new-root.io/clusters/34", "root:org")
	}
}

func TestUpsertWorkspace(t *testing.T) {
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("34"))
	target.UpsertLogicalCluster("root", newLogicalCluster("44"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "34", true)

	target.UpsertWorkspace("root", newWorkspace("org", "root", "44"))
	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, found, "root", "44", true)
}

func validateLookupOutput(t *testing.T, path logicalcluster.Path, shard string, cluster logicalcluster.Name, found bool, expectedShard string, expectedCluster logicalcluster.Name, expectToFind bool) {
	t.Helper()

	if found != expectToFind {
		t.Fatalf("found != expectedToFind, for %q path", path)
	}
	if cluster != expectedCluster {
		t.Fatalf("unexpected logical cluster = %v, expected = %v for %q path", cluster, expectedCluster, path)
	}
	if shard != expectedShard {
		t.Fatalf("unexpected shard = %v, expected = %v, for %q path", shard, expectedShard, path)
	}
}

func newWorkspace(name, cluster, scheduledCluster string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"kcp.io/cluster": cluster}},
		Spec:       tenancyv1alpha1.WorkspaceSpec{Cluster: scheduledCluster},
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	}
}

func newLogicalCluster(cluster string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Annotations: map[string]string{"kcp.io/cluster": cluster}},
	}
}
