/*
Copyright 2023 The kcp Authors.

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// Naming convention used throughout this file:
//   - workspace names are plain words:      "org", "rh", "mount"
//   - logical cluster IDs are "lc-"prefixed: "lc-org", "lc-rh"
//     (the special root logical cluster keeps its real name, "root")
//
// A path segment can be EITHER a workspace name or a logical cluster ID, and
// the "lc-" prefix makes it obvious which kind a given path segment is.

type shardStub struct {
	name string
	url  string
}

func TestLookup(t *testing.T) {
	t.Parallel()

	// Shared fixtures for the multi-shard scenarios. The workspace hierarchy is
	// root:org:rh, where the "org" workspace resolves to logical cluster
	// "lc-org" and "rh" to logical cluster "lc-rh". The hierarchy is
	// deliberately split across shards: the "org" workspace lives on shard
	// "root" while its child "rh" lives on shard "beta". This lets the cases
	// below exercise how a path resolves when a canonical workspace path and a
	// raw logical cluster ID are mixed.
	threeShards := []shardStub{
		{name: "root", url: "https://root.kcp.dev"},
		{name: "beta", url: "https://beta.kcp.dev"},
		{name: "gama", url: "https://gama.kcp.dev"},
	}
	splitWorkspaces := map[string][]*tenancyv1alpha1.Workspace{
		"root": {newWorkspace("org", "root", "lc-org")},
		"beta": {newWorkspace("rh", "lc-org", "lc-rh")},
	}
	splitLogicalClusters := map[string][]*corev1alpha1.LogicalCluster{
		"root": {newLogicalCluster("root")},
		"beta": {newLogicalCluster("lc-org")},
		"gama": {newLogicalCluster("lc-rh")},
	}

	scenarios := []struct {
		name                           string
		targetPath                     logicalcluster.Path
		initialShardsToUpsert          []shardStub
		initialWorkspacesToUpsert      map[string][]*tenancyv1alpha1.Workspace
		initialLogicalClustersToUpsert map[string][]*corev1alpha1.LogicalCluster

		expectedShard   string
		expectedCluster logicalcluster.Name
		expectFound     bool
		expectedURL     string
		expectedError   int
	}{
		{
			name: "an empty indexer is usable",
		},
		{
			name:       "empty path",
			targetPath: logicalcluster.NewPath(""),
		},
		{
			// A workspace still in the Scheduling phase is not indexed, so
			// "root:org" cannot be resolved yet.
			name: "a workspace must be scheduled to be considered",
			initialShardsToUpsert: []shardStub{{
				name: "root",
				url:  "https://root.kcp.dev",
			}},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {func() *tenancyv1alpha1.Workspace {
					ws := newWorkspace("org", "root", "lc-org")
					ws.Status.Phase = corev1alpha1.LogicalClusterPhaseScheduling
					return ws
				}()},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root": {newLogicalCluster("root"), newLogicalCluster("lc-org")},
			},
			targetPath: logicalcluster.NewPath("root:org"),
		},
		{
			// Canonical path, single shard: "root:org" walks workspace names to
			// logical cluster "lc-org".
			name: "single shard: a logical cluster for root:org workspace is found",
			initialShardsToUpsert: []shardStub{{
				name: "root",
				url:  "https://root.kcp.dev",
			}},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "lc-org"), newWorkspace("rh", "lc-org", "lc-rh")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root": {newLogicalCluster("root"), newLogicalCluster("lc-org"), newLogicalCluster("lc-rh")},
			},
			targetPath:      logicalcluster.NewPath("root:org"),
			expectFound:     true,
			expectedCluster: "lc-org",
			expectedShard:   "root",
		},
		{
			// Canonical path, cluster living on a different shard than its root.
			name: "multiple shards: a logical cluster for root:org workspace is found",
			initialShardsToUpsert: []shardStub{
				{name: "root", url: "https://root.kcp.dev"},
				{name: "beta", url: "https://beta.kcp.dev"},
			},
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "lc-org"), newWorkspace("rh", "lc-org", "lc-rh")},
			},
			initialLogicalClustersToUpsert: map[string][]*corev1alpha1.LogicalCluster{
				"root": {newLogicalCluster("root")},
				"beta": {newLogicalCluster("lc-org"), newLogicalCluster("lc-rh")},
			},
			targetPath:      logicalcluster.NewPath("root:org"),
			expectFound:     true,
			expectedCluster: "lc-org",
			expectedShard:   "beta",
		},
		{
			// Full canonical path across the split hierarchy.
			name:                           "multiple shards: a logical cluster for root:org:rh workspace is found",
			initialShardsToUpsert:          threeShards,
			initialWorkspacesToUpsert:      splitWorkspaces,
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("root:org:rh"),
			expectFound:                    true,
			expectedCluster:                "lc-rh",
			expectedShard:                  "gama",
		},
		{
			// Mixing: the path starts with a raw logical cluster ID ("lc-org")
			// instead of the canonical "root:org" prefix, followed by the
			// workspace name "rh". Only the FIRST segment may be a cluster ID.
			name:                           "multiple shards: a logical cluster for lc-org:rh workspace is found",
			initialShardsToUpsert:          threeShards,
			initialWorkspacesToUpsert:      splitWorkspaces,
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("lc-org:rh"),
			expectFound:                    true,
			expectedCluster:                "lc-rh",
			expectedShard:                  "gama",
		},
		{
			// Leading segment is neither a known cluster ID nor a shard root.
			name:                           "multiple shards: a logical cluster for does-not-exists:rh workspace is NOT found",
			initialShardsToUpsert:          threeShards,
			initialWorkspacesToUpsert:      splitWorkspaces,
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("does-not-exists:rh"),
			expectFound:                    false,
		},
		{
			// Mixing rejected: a cluster ID ("lc-org") in the MIDDLE of the path
			// is not honored — the cluster-ID shortcut only works as the leading
			// segment. Contrast with the "lc-org:rh" case above.
			name:                           "multiple shards: a logical cluster for root:lc-org:rh workspace is NOT found",
			initialShardsToUpsert:          threeShards,
			initialWorkspacesToUpsert:      splitWorkspaces,
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("root:lc-org:rh"),
			expectFound:                    false,
		},
		{
			name:                  "multiple shards: lc-org:mount workspace is a mount with URL",
			initialShardsToUpsert: threeShards,
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "lc-org")},
				"beta": {
					withURL(
						withPhase(
							newWorkspaceWithMount("mount", "lc-org", "", tenancyv1alpha1.ObjectReference{
								Kind:       "KubeCluster",
								Name:       "prod-cluster",
								APIVersion: "proxy.kcp.dev/v1alpha1",
							}),
							"Ready"),
						"https://kcp.dev.local/services/custom-url/proxy")},
			},
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("lc-org:mount"),
			expectFound:                    true,
			expectedCluster:                "",
			expectedShard:                  "",
			expectedURL:                    "https://kcp.dev.local/services/custom-url/proxy",
		},
		{
			name:                  "multiple shards: lc-org:rh workspace is a mount, but phase is Unavailable",
			initialShardsToUpsert: threeShards,
			initialWorkspacesToUpsert: map[string][]*tenancyv1alpha1.Workspace{
				"root": {newWorkspace("org", "root", "lc-org")},
				"beta": {withURL(withPhase(newWorkspaceWithMount("rh", "lc-org", "", tenancyv1alpha1.ObjectReference{
					Kind:       "KubeCluster",
					Name:       "prod-cluster",
					APIVersion: "proxy.kcp.dev/v1alpha1",
				}), corev1alpha1.LogicalClusterPhaseUnavailable), "https://kcp.dev.local/services/custom-url/proxy")},
			},
			initialLogicalClustersToUpsert: splitLogicalClusters,
			targetPath:                     logicalcluster.NewPath("lc-org:rh"),
			expectFound:                    true,
			expectedError:                  503,
			expectedCluster:                "",
			expectedShard:                  "",
			expectedURL:                    "https://kcp.dev.local/services/custom-url/proxy",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
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
			if !scenario.expectFound && found {
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
			if r.URL != scenario.expectedURL {
				t.Errorf("unexpected url = %v, for path = %v, expected = %v", r.URL, scenario.targetPath, scenario.expectedURL)
			}
			if r.ErrorCode != scenario.expectedError {
				t.Errorf("unexpected error code = %v, for path = %v, expected = %v", r.ErrorCode, scenario.targetPath, scenario.expectedError)
			}
		})
	}
}

func TestDeleteShard(t *testing.T) {
	t.Parallel()
	target := New(nil)

	// ensure deleting not existing shard won't blow up
	target.DeleteShard("root")

	target.UpsertShard("root", "https://root.io")
	target.UpsertShard("amber", "https://amber.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertWorkspace("root", newWorkspace("org1", "root", "lc-org1"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))
	target.UpsertLogicalCluster("amber", newLogicalCluster("lc-org1"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "amber", "lc-org1", "", true)

	// delete the shard and ensure we cannot look up a path on it
	target.DeleteShard("amber")
	r, found = target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "", "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org", "", true)
}

func TestDeleteLogicalCluster(t *testing.T) {
	t.Parallel()
	target := New(nil)

	// ensure deleting not existent logical cluster won't blow up
	target.DeleteLogicalCluster("root", newLogicalCluster("lc-org"))

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org", "", true)

	// ensure that after deleting the logical cluster it cannot be looked up
	target.DeleteLogicalCluster("root", newLogicalCluster("lc-org"))

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "", "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "root", "", true)
}

// TestDeleteLogicalClusterScrubsAllMaps guards against the bug where
// DeleteLogicalCluster cleaned only 2 of 7 per-cluster maps, leaving stale
// entries whenever the LogicalCluster delete event arrived before (or
// without) the matching Workspace delete event.
func TestDeleteLogicalClusterScrubsAllMaps(t *testing.T) {
	t.Parallel()
	target := New(nil)

	target.UpsertShard("root", "https://root.io")

	// "lc-org" exists both as a child (org workspace under root points at it)
	// and as a parent (it has its own sub-workspaces "mounted" and "broken").
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertWorkspace("root", newWorkspaceWithMount("mounted", "lc-org", "lc-mounted",
		tenancyv1alpha1.ObjectReference{APIVersion: "v1", Kind: "Cluster", Name: "m"}))
	target.UpsertWorkspace("root", withPhase(newWorkspace("broken", "lc-org", "lc-broken"),
		corev1alpha1.LogicalClusterPhaseUnavailable))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))

	// Sanity: all 7 maps must hold an entry that references "lc-org".
	assertHasCluster := func(name string, presence bool) {
		t.Helper()
		c := logicalcluster.Name("lc-org")
		_, child := target.shardClusterWorkspaceName["root"][c]
		_, parent := target.shardClusterParentCluster["root"][c]
		_, asParentInName := target.shardClusterWorkspaceNameCluster["root"][c]
		_, asParentInMount := target.shardClusterWorkspaceMount["root"][c]
		_, asParentInError := target.shardClusterWorkspaceNameErrorCode["root"][c]
		_, asType := target.shardClusterWorkspaceType["root"][c]
		_, asShard := target.clusterShards[c]
		got := map[string]bool{
			"shardClusterWorkspaceName":          child,
			"shardClusterParentCluster":          parent,
			"shardClusterWorkspaceNameCluster":   asParentInName,
			"shardClusterWorkspaceMount":         asParentInMount,
			"shardClusterWorkspaceNameErrorCode": asParentInError,
			"shardClusterWorkspaceType":          asType,
			"clusterShards":                      asShard,
		}
		for k, v := range got {
			if v != presence {
				t.Errorf("[%s] map %q for cluster %q: got presence=%v, want %v", name, k, c, v, presence)
			}
		}
	}
	assertHasCluster("pre-delete", true)

	// Simulate the race: LogicalCluster delete arrives before any of the
	// Workspace delete events.
	target.DeleteLogicalCluster("root", newLogicalCluster("lc-org"))

	// All 7 maps must now be free of any reference to "lc-org".
	assertHasCluster("post-delete", false)
}

// TestDeleteLogicalCluster_ScrubsAllMaps verifies DeleteLogicalCluster removes
// every per-cluster entry, including ones populated by UpsertWorkspace before
// the matching Workspace delete event is observed. The leak this guards against
// is that the LogicalCluster delete may race ahead of the Workspace delete (or
// the Workspace event may be missed entirely on the front proxy informer), in
// which case the per-cluster maps would otherwise retain entries indefinitely.
func TestDeleteLogicalCluster_ScrubsAllMaps(t *testing.T) {
	t.Parallel()
	target := New(nil)
	target.UpsertShard("root", "https://root.io")
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))

	// Parent "root" contains workspace "org" with scheduled cluster "lc-org",
	// plus an unavailable workspace and a mounted one — exercises every map.
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertWorkspace("root", withPhase(newWorkspace("bad", "root", "lc-bad"), corev1alpha1.LogicalClusterPhaseUnavailable))
	target.UpsertWorkspace("root", newWorkspaceWithMount("mnt", "root", "lc-mnt", tenancyv1alpha1.ObjectReference{Name: "ref"}))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-bad"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-mnt"))

	// Sanity: every map has the parent's bucket populated.
	if _, ok := target.shardClusterWorkspaceNameCluster["root"][logicalcluster.Name("root")]; !ok {
		t.Fatal("setup: shardClusterWorkspaceNameCluster[root][root] should be populated")
	}
	if _, ok := target.shardClusterWorkspaceMount["root"][logicalcluster.Name("root")]; !ok {
		t.Fatal("setup: shardClusterWorkspaceMount[root][root] should be populated")
	}
	if _, ok := target.shardClusterWorkspaceNameErrorCode["root"][logicalcluster.Name("root")]; !ok {
		t.Fatal("setup: shardClusterWorkspaceNameErrorCode[root][root] should be populated")
	}
	if _, ok := target.shardClusterWorkspaceName["root"][logicalcluster.Name("lc-org")]; !ok {
		t.Fatal("setup: shardClusterWorkspaceName[root][lc-org] should be populated")
	}
	if _, ok := target.shardClusterParentCluster["root"][logicalcluster.Name("lc-org")]; !ok {
		t.Fatal("setup: shardClusterParentCluster[root][lc-org] should be populated")
	}

	// Delete the CHILD logical cluster directly, without first deleting the
	// Workspace CR. This is the out-of-order case the cleanup must handle.
	target.DeleteLogicalCluster("root", newLogicalCluster("lc-org"))

	if _, ok := target.shardClusterWorkspaceName["root"][logicalcluster.Name("lc-org")]; ok {
		t.Error("shardClusterWorkspaceName[root][lc-org] leaked after child LogicalCluster delete")
	}
	if _, ok := target.shardClusterParentCluster["root"][logicalcluster.Name("lc-org")]; ok {
		t.Error("shardClusterParentCluster[root][lc-org] leaked after child LogicalCluster delete")
	}
	if _, ok := target.clusterShards[logicalcluster.Name("lc-org")]; ok {
		t.Error("clusterShards[lc-org] leaked after LogicalCluster delete")
	}

	// Delete the PARENT logical cluster while child workspace entries still exist
	// in the parent-keyed buckets. The whole bucket should be dropped.
	target.DeleteLogicalCluster("root", newLogicalCluster("root"))

	if _, ok := target.shardClusterWorkspaceNameCluster["root"][logicalcluster.Name("root")]; ok {
		t.Error("shardClusterWorkspaceNameCluster[root][root] leaked after parent LogicalCluster delete")
	}
	if _, ok := target.shardClusterWorkspaceMount["root"][logicalcluster.Name("root")]; ok {
		t.Error("shardClusterWorkspaceMount[root][root] leaked after parent LogicalCluster delete")
	}
	if _, ok := target.shardClusterWorkspaceNameErrorCode["root"][logicalcluster.Name("root")]; ok {
		t.Error("shardClusterWorkspaceNameErrorCode[root][root] leaked after parent LogicalCluster delete")
	}
	if _, ok := target.shardClusterWorkspaceType["root"][logicalcluster.Name("root")]; ok {
		t.Error("shardClusterWorkspaceType[root][root] leaked after parent LogicalCluster delete")
	}
}

func TestDeleteWorkspace(t *testing.T) {
	t.Parallel()
	target := New(nil)

	// ensure deleting not existent workspace won't blow up
	target.DeleteWorkspace("root", newWorkspace("org", "root", "lc-org"))

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertWorkspace("root", newWorkspace("org1", "root", "lc-org1"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org1"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org", "", true)

	target.DeleteWorkspace("root", newWorkspace("org", "root", "lc-org"))

	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "", "", "", false)

	r, found = target.Lookup(logicalcluster.NewPath("root:org1"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org1", "", true)
}

func TestUpsertLogicalCluster(t *testing.T) {
	t.Parallel()
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertShard("amber", "https://amber.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org", "", true)

	target.UpsertLogicalCluster("amber", newLogicalCluster("lc-org"))
	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "amber", "lc-org", "", true)
}

// Since LookupURL uses Lookup method the following test is just a smoke tests.
func TestLookupURL(t *testing.T) {
	t.Parallel()
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))

	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://root.io/clusters/lc-org" {
		t.Fatalf("unexpected url. returned = %v, expected = %v for %q path", r.URL, "https://root.io/clusters/lc-org", "root:org")
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
	t.Parallel()
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))

	r, found := target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://root.io/clusters/lc-org" {
		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://root.io/clusters/lc-org", "root:org")
	}

	target.UpsertShard("root", "https://new-root.io")
	r, found = target.LookupURL(logicalcluster.NewPath("root:org"))
	if !found {
		t.Fatalf("expected to find a URL for %q path", "root:org")
	}
	if r.URL != "https://new-root.io/clusters/lc-org" {
		t.Fatalf("unexpected url = %v returned, expected = %v for %q path", r.URL, "https://new-root.io/clusters/lc-org", "root:org")
	}
}

func TestUpsertWorkspace(t *testing.T) {
	t.Parallel()
	target := New(nil)

	target.UpsertShard("root", "https://root.io")
	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("root"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org"))
	target.UpsertLogicalCluster("root", newLogicalCluster("lc-org-v2"))

	r, found := target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org", "", true)

	target.UpsertWorkspace("root", newWorkspace("org", "root", "lc-org-v2"))
	r, found = target.Lookup(logicalcluster.NewPath("root:org"))
	validateLookupOutput(t, logicalcluster.NewPath("root:org"), r.Shard, r.Cluster, r.URL, found, "root", "lc-org-v2", "", true)
}

func validateLookupOutput(t *testing.T, path logicalcluster.Path, shard string, cluster logicalcluster.Name, url string, found bool, expectedShard string, expectedCluster logicalcluster.Name, expectedURL string, expectToFind bool) {
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
	if url != expectedURL {
		t.Fatalf("unexpected url = %v, expected = %v, for %q path", url, expectedURL, path)
	}
}

// newWorkspace builds a Workspace named workspaceName that lives in the
// parentClusterID logical cluster and schedules its own contents to the
// childClusterID logical cluster (spec.Cluster). By convention logical cluster
// IDs are "lc-"prefixed and workspaceName is a plain word.
func newWorkspace(workspaceName, parentClusterID, childClusterID string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: workspaceName, Annotations: map[string]string{"kcp.io/cluster": parentClusterID}},
		Spec:       tenancyv1alpha1.WorkspaceSpec{Cluster: childClusterID},
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	}
}

func newWorkspaceWithMount(workspaceName, parentClusterID, childClusterID string, ref tenancyv1alpha1.ObjectReference) *tenancyv1alpha1.Workspace {
	ws := newWorkspace(workspaceName, parentClusterID, childClusterID)
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

// newLogicalCluster builds a LogicalCluster identified by logicalClusterID
// (the kcp.io/cluster annotation). By convention these IDs are "lc-"prefixed.
func newLogicalCluster(logicalClusterID string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Annotations: map[string]string{"kcp.io/cluster": logicalClusterID}},
	}
}
