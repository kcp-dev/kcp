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

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
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
			require.Equal(t, tc.accepted, accepted, "accepted")
			if !tc.accepted {
				return
			}
			require.Equal(t, tc.wildcard, cluster.Wildcard, "wildcard")
			if !tc.wildcard {
				require.Equal(t, tc.clusterName, cluster.Name.String(), "cluster")
			}
			require.Equal(t, tc.prefixToTrim, prefixToTrim, "prefixToTrim")
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
	require.Len(t, result, 2, "expected 2 shards after dedupe")
	require.Equal(t, "system:shard", logicalcluster.From(&result[0]).String(), "expected the system:shard copy of alpha to win")
	require.Equal(t, "beta", result[1].GetName(), "expected beta to survive")
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
		require.NotContains(t, anns, key, "annotation %q must be stripped", key)
	}
	require.Equal(t, "system:shard", anns[logicalcluster.AnnotationKey], "the kcp.io/cluster annotation must be kept")
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

	require.Len(t, got, 1, "expected 1 forwarded event: %v", got)
	require.NotEqual(t, watch.Deleted, got[0].Type, "legacy copy deletion was forwarded as DELETED; the shard-owned copy is still present")
	obj, ok := got[0].Object.(*unstructured.Unstructured)
	require.True(t, ok, "expected an *unstructured.Unstructured, got %T", got[0].Object)
	require.Equal(t, "system:shard", logicalcluster.From(obj).String(), "expected the shard-owned copy to be published")
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

	require.NotEmpty(t, got, "expected events, got none")
	require.Equal(t, watch.Deleted, got[len(got)-1].Type, "expected the final event to be DELETED")
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
		require.Equal(t, "system:shard", logicalcluster.From(obj).String(), "published the losing copy")
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

	require.Len(t, got, 1, "expected the bookmark to be forwarded unchanged: %v", got)
	require.Equal(t, watch.Bookmark, got[0].Type, "expected the bookmark to be forwarded unchanged")
	require.Empty(t, w.copies, "bookmark was folded into the shard state")
}

func TestMutableFieldUpdate(t *testing.T) {
	t.Parallel()

	const cordonKey = corev1alpha1.ShardUnschedulableAnnotationKey

	shard := func(mutate func(obj map[string]any)) *unstructured.Unstructured {
		obj := map[string]any{
			"apiVersion": "core.kcp.io/v1alpha1",
			"kind":       "Shard",
			"metadata": map[string]any{
				"name":        "shard-1",
				"annotations": map[string]any{"kcp.io/cluster": "system:shard"},
			},
			"spec": map[string]any{"baseURL": "https://shard-1"},
		}
		if mutate != nil {
			mutate(obj)
		}
		return &unstructured.Unstructured{Object: obj}
	}
	limits := map[string]any{"hard": map[string]any{"workspaces": "10"}}

	scenarios := []struct {
		name        string
		desired     *unstructured.Unstructured
		wantErr     bool
		wantValues  []any
		wantPresent []bool
	}{
		{
			name:        "no change",
			desired:     shard(nil),
			wantValues:  []any{nil, nil},
			wantPresent: []bool{false, false},
		},
		{
			name: "cordoning is allowed",
			desired: shard(func(obj map[string]any) {
				obj["metadata"].(map[string]any)["annotations"].(map[string]any)[cordonKey] = "true"
			}),
			wantValues:  []any{"true", nil},
			wantPresent: []bool{true, false},
		},
		{
			name: "setting resourceLimits is allowed",
			desired: shard(func(obj map[string]any) {
				obj["spec"].(map[string]any)["resourceLimits"] = limits
			}),
			wantValues:  []any{nil, limits},
			wantPresent: []bool{false, true},
		},
		{
			name: "cordoning and resourceLimits together are allowed",
			desired: shard(func(obj map[string]any) {
				obj["metadata"].(map[string]any)["annotations"].(map[string]any)[cordonKey] = "true"
				obj["spec"].(map[string]any)["resourceLimits"] = limits
			}),
			wantValues:  []any{"true", limits},
			wantPresent: []bool{true, true},
		},
		{
			name: "changing a shard-owned spec field is forbidden",
			desired: shard(func(obj map[string]any) {
				obj["spec"].(map[string]any)["baseURL"] = "https://hijacked"
			}),
			wantErr: true,
		},
		{
			name: "changing an unrelated annotation is forbidden",
			desired: shard(func(obj map[string]any) {
				obj["metadata"].(map[string]any)["annotations"].(map[string]any)["evil"] = "yes"
			}),
			wantErr: true,
		},
		{
			name: "changing status is forbidden",
			desired: shard(func(obj map[string]any) {
				obj["status"] = map[string]any{"used": map[string]any{"workspaces": "0"}}
			}),
			wantErr: true,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			values, present, err := mutableFieldUpdate("shard-1", shard(nil), scenario.desired)
			if scenario.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				if !apierrors.IsForbidden(err) {
					t.Fatalf("expected a Forbidden error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(scenario.wantValues, values); diff != "" {
				t.Errorf("unexpected values (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(scenario.wantPresent, present); diff != "" {
				t.Errorf("unexpected presence (-want +got):\n%s", diff)
			}
		})
	}
}
