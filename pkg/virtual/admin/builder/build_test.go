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

package builder

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestDigestURL(t *testing.T) {
	t.Parallel()
	const prefix = "/services/admin"

	tests := []struct {
		urlPath      string
		accepted     bool
		wildcard     bool
		clusterName  string
		prefixToTrim string
	}{
		{urlPath: "/services/admin", accepted: true, clusterName: "root", prefixToTrim: "/services/admin"},
		{urlPath: "/services/admin/apis/core.kcp.io/v1alpha1/shards", accepted: true, clusterName: "root", prefixToTrim: "/services/admin"},
		{urlPath: "/services/admin/clusters/*/apis/core.kcp.io/v1alpha1/shards", accepted: true, wildcard: true, prefixToTrim: "/services/admin/clusters/*"},
		{urlPath: "/services/admin/clusters/myws/apis/core.kcp.io/v1alpha1/shards", accepted: true, clusterName: "myws", prefixToTrim: "/services/admin/clusters/myws"},
		{urlPath: "/services/adminfoo/apis", accepted: false},
		{urlPath: "/services/other", accepted: false},
	}

	for _, tc := range tests {
		t.Run(tc.urlPath, func(t *testing.T) {
			t.Parallel()
			cluster, prefixToTrim, accepted := digestURL(tc.urlPath, prefix)
			if accepted != tc.accepted {
				t.Fatalf("accepted = %v, want %v", accepted, tc.accepted)
			}
			if !tc.accepted {
				return
			}
			if cluster.Wildcard != tc.wildcard {
				t.Errorf("wildcard = %v, want %v", cluster.Wildcard, tc.wildcard)
			}
			if !tc.wildcard && cluster.Name.String() != tc.clusterName {
				t.Errorf("cluster = %q, want %q", cluster.Name, tc.clusterName)
			}
			if prefixToTrim != tc.prefixToTrim {
				t.Errorf("prefixToStrip = %q, want %q", prefixToTrim, tc.prefixToTrim)
			}
		})
	}
}

func shardObj(name, cluster string, annotations map[string]string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.kcp.io/v1alpha1",
		"kind":       "Shard",
		"metadata": map[string]interface{}{
			"name": name,
			"annotations": map[string]interface{}{
				logicalcluster.AnnotationKey: cluster,
			},
		},
	}}
	for k, v := range annotations {
		anns := u.GetAnnotations()
		anns[k] = v
		u.SetAnnotations(anns)
	}
	return u
}

func TestDedupeShardsPrefersSystemShard(t *testing.T) {
	t.Parallel()
	items := []unstructured.Unstructured{
		shardObj("alpha", "root", nil),
		shardObj("alpha", "system:shard", nil),
		shardObj("beta", "root", nil),
	}
	result := dedupeShards(items)
	if len(result) != 2 {
		t.Fatalf("expected 2 shards after dedupe, got %d", len(result))
	}
	if got := logicalcluster.From(&result[0]).String(); got != "system:shard" {
		t.Errorf("expected the system:shard copy of alpha to win, got cluster %q", got)
	}
	if result[1].GetName() != "beta" {
		t.Errorf("expected beta to survive, got %q", result[1].GetName())
	}
}

func TestFixupShardStripsCacheBookkeeping(t *testing.T) {
	t.Parallel()
	u := shardObj("alpha", "system:shard", map[string]string{
		"kcp.io/shard":                           "alpha",
		"cache.kcp.io/original-resource-version": "42",
		"cache.kcp.io/original-resource-UID":     "abc",
	})
	fixupShard(&u)
	anns := u.GetAnnotations()
	for _, key := range []string{"kcp.io/shard", "cache.kcp.io/original-resource-version", "cache.kcp.io/original-resource-UID"} {
		if _, ok := anns[key]; ok {
			t.Errorf("annotation %q must be stripped", key)
		}
	}
	if anns[logicalcluster.AnnotationKey] != "system:shard" {
		t.Error("the kcp.io/cluster annotation must be kept")
	}
}

// collectFixupWatch drains a fixupWatch until it goes quiet, returning the
// events it forwarded.
func collectFixupWatch(t *testing.T, w *fixupWatch, source *watch.FakeWatcher, events []watch.Event) []watch.Event {
	t.Helper()

	for _, event := range events {
		source.Action(event.Type, event.Object)
	}
	source.Stop()

	var got []watch.Event
	for event := range w.ResultChan() {
		got = append(got, event)
	}
	return got
}

func newTestFixupWatch(t *testing.T, seed []unstructured.Unstructured) (*fixupWatch, *watch.FakeWatcher) {
	t.Helper()

	source := watch.NewFake()
	// NewFake blocks senders until a receiver is ready, which deadlocks a
	// single-goroutine test; the fixupWatch pump is that receiver.
	return newFixupWatch(context.Background(), source, seed), source
}

// TestFixupWatchKeepsShardOnLegacyCopyDelete covers the front-proxy failure
// mode: while a shard migrates, the replication controller prunes the legacy
// root copy from the cache. A name-keyed consumer must not conclude that the
// shard is gone, because the shard-owned copy is still there.
func TestFixupWatchKeepsShardOnLegacyCopyDelete(t *testing.T) {
	t.Parallel()

	legacy := shardObj("alpha", "root", nil)
	owned := shardObj("alpha", "system:shard", nil)

	w, source := newTestFixupWatch(t, []unstructured.Unstructured{legacy, owned})
	got := collectFixupWatch(t, w, source, []watch.Event{
		{Type: watch.Deleted, Object: &legacy},
	})

	if len(got) != 1 {
		t.Fatalf("expected 1 forwarded event, got %d: %v", len(got), got)
	}
	if got[0].Type == watch.Deleted {
		t.Fatalf("legacy copy deletion was forwarded as DELETED; the shard-owned copy is still present")
	}
	obj, ok := got[0].Object.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("expected an *unstructured.Unstructured, got %T", got[0].Object)
	}
	if cluster := logicalcluster.From(obj).String(); cluster != "system:shard" {
		t.Errorf("expected the shard-owned copy to be published, got cluster %q", cluster)
	}
}

// TestFixupWatchDeletesShardWhenLastCopyGoes checks the other half: once no
// copy is left the shard really is gone and DELETED must be forwarded.
func TestFixupWatchDeletesShardWhenLastCopyGoes(t *testing.T) {
	t.Parallel()

	legacy := shardObj("alpha", "root", nil)
	owned := shardObj("alpha", "system:shard", nil)

	w, source := newTestFixupWatch(t, []unstructured.Unstructured{legacy, owned})
	got := collectFixupWatch(t, w, source, []watch.Event{
		{Type: watch.Deleted, Object: &legacy},
		{Type: watch.Deleted, Object: &owned},
	})

	if len(got) == 0 {
		t.Fatalf("expected events, got none")
	}
	last := got[len(got)-1]
	if last.Type != watch.Deleted {
		t.Fatalf("expected the final event to be DELETED, got %s", last.Type)
	}
}

// TestFixupWatchIgnoresLosingCopyChange makes sure updates to the legacy copy
// do not overwrite the shard-owned content in the consumer's cache.
func TestFixupWatchIgnoresLosingCopyChange(t *testing.T) {
	t.Parallel()

	legacy := shardObj("alpha", "root", nil)
	owned := shardObj("alpha", "system:shard", nil)

	w, source := newTestFixupWatch(t, []unstructured.Unstructured{legacy, owned})

	changed := legacy.DeepCopy()
	changed.SetLabels(map[string]string{"stale": "true"})

	got := collectFixupWatch(t, w, source, []watch.Event{
		{Type: watch.Modified, Object: changed},
	})

	for _, event := range got {
		obj, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if cluster := logicalcluster.From(obj).String(); cluster != "system:shard" {
			t.Errorf("published the losing copy from cluster %q", cluster)
		}
	}
}

// TestFixupWatchPassesBookmarksThrough keeps watch resumption working: a
// bookmark carries only a resourceVersion and must not be folded into the
// shard state.
func TestFixupWatchPassesBookmarksThrough(t *testing.T) {
	t.Parallel()

	w, source := newTestFixupWatch(t, nil)

	bookmark := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "core.kcp.io/v1alpha1",
		"kind":       "Shard",
		"metadata":   map[string]interface{}{"resourceVersion": "42"},
	}}
	got := collectFixupWatch(t, w, source, []watch.Event{
		{Type: watch.Bookmark, Object: bookmark},
	})

	if len(got) != 1 || got[0].Type != watch.Bookmark {
		t.Fatalf("expected the bookmark to be forwarded unchanged, got %v", got)
	}
	if len(w.copies) != 0 {
		t.Errorf("bookmark was folded into the shard state: %v", w.copies)
	}
}
