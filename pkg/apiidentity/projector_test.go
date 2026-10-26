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

package apiidentity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

const (
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// export is an APIExport holding the identity, created at epoch plus the
// given number of minutes.
func export(cluster, name, hash string, createdMinutes int) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			UID:               types.UID("uid-" + cluster + "-" + name),
			Annotations:       map[string]string{logicalcluster.AnnotationKey: cluster},
			CreationTimestamp: metav1.NewTime(epoch.Add(time.Duration(createdMinutes) * time.Minute)),
		},
		Status: apisv1alpha2.APIExportStatus{IdentityHash: hash},
	}
}

func ref(cluster, name string) adminv1alpha1.ExportReference {
	return adminv1alpha1.ExportReference{Cluster: cluster, Name: name, UID: "uid-" + cluster + "-" + name}
}

func TestProject(t *testing.T) {
	t.Parallel()

	t.Run("no exports, no entry", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, Project(hashA, nil, IsCoreExport))
	})

	t.Run("the oldest export owns, the others hold, in age order", func(t *testing.T) {
		t.Parallel()
		entry := Project(hashA, []*apisv1alpha2.APIExport{
			export("ws-c", "wildwest", hashA, 5),
			export("ws-a", "wildwest", hashA, 1),
			export("ws-b", "wildwest", hashA, 3),
		}, IsCoreExport)
		require.Equal(t, hashA, entry.Name)
		require.Equal(t, ref("ws-a", "wildwest"), entry.Status.Owner)
		require.Equal(t, []adminv1alpha1.Holder{
			{ExportReference: ref("ws-b", "wildwest"), Since: metav1.NewTime(epoch.Add(3 * time.Minute))},
			{ExportReference: ref("ws-c", "wildwest"), Since: metav1.NewTime(epoch.Add(5 * time.Minute))},
		}, entry.Status.Holders)
		require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityShared))
		require.False(t, entry.Status.Core)
		require.Equal(t, adminv1alpha1.Labels(ref("ws-a", "wildwest"), false), entry.Labels)
		require.Equal(t, epoch.Add(time.Minute), entry.CreationTimestamp.Time)
	})

	t.Run("same age: cluster, then name, decides", func(t *testing.T) {
		t.Parallel()
		entry := Project(hashA, []*apisv1alpha2.APIExport{
			export("ws-b", "a", hashA, 1),
			export("ws-a", "z", hashA, 1),
			export("ws-a", "b", hashA, 1),
		}, IsCoreExport)
		require.Equal(t, ref("ws-a", "b"), entry.Status.Owner)
	})

	t.Run("a sole holder is not shared", func(t *testing.T) {
		t.Parallel()
		entry := Project(hashA, []*apisv1alpha2.APIExport{export("ws-a", "wildwest", hashA, 1)}, IsCoreExport)
		require.Empty(t, entry.Status.Holders)
		require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityShared))
	})

	t.Run("the core exports of root are core", func(t *testing.T) {
		t.Parallel()
		entry := Project(hashA, []*apisv1alpha2.APIExport{export("root", "tenancy.kcp.io", hashA, 1)}, IsCoreExport)
		require.True(t, entry.Status.Core)
		require.Equal(t, "true", entry.Labels[adminv1alpha1.APIIdentityCoreLabel])
		require.False(t, Project(hashA, []*apisv1alpha2.APIExport{export("org", "tenancy.kcp.io", hashA, 1)}, IsCoreExport).Status.Core)
	})
}

// recorder collects the events a handler gets.
type recorder struct {
	events []string
}

func (r *recorder) OnAdd(obj interface{}, _ bool) { r.record("add", obj) }
func (r *recorder) OnUpdate(_, obj interface{})   { r.record("update", obj) }
func (r *recorder) OnDelete(obj interface{})      { r.record("delete", obj) }
func (r *recorder) record(typ string, obj interface{}) {
	r.events = append(r.events, typ+" "+obj.(*adminv1alpha1.APIIdentity).Name[:1]+" owner="+obj.(*adminv1alpha1.APIIdentity).Status.Owner.Cluster)
}

func TestProjectorFollowsExports(t *testing.T) {
	t.Parallel()
	p := NewProjector(IsCoreExport)
	rec := &recorder{}
	p.AddEventHandler(rec)

	t.Log("an export without identity is not an entry")
	p.OnAdd(export("ws-a", "wildwest", "", 1), true)
	require.Empty(t, p.List(nil))

	t.Log("the identity appears with the first export holding it")
	p.OnUpdate(export("ws-a", "wildwest", "", 1), export("ws-a", "wildwest", hashA, 1))
	entry, ok := p.Get(hashA)
	require.True(t, ok)
	require.Equal(t, ref("ws-a", "wildwest"), entry.Status.Owner)
	require.Equal(t, "1", entry.ResourceVersion)

	t.Log("a second export with the same hash becomes a holder")
	p.OnAdd(export("ws-b", "wildwest", hashA, 2), false)
	entry, _ = p.Get(hashA)
	require.Len(t, entry.Status.Holders, 1)
	require.Equal(t, "2", entry.ResourceVersion)

	t.Log("re-delivering the same export changes nothing")
	p.OnUpdate(export("ws-b", "wildwest", hashA, 2), export("ws-b", "wildwest", hashA, 2))
	entry, _ = p.Get(hashA)
	require.Equal(t, "2", entry.ResourceVersion)
	require.Equal(t, "2", p.ResourceVersion())

	t.Log("deleting the owner passes the identity to the holder")
	p.OnDelete(cache.DeletedFinalStateUnknown{Key: "ws-a|wildwest", Obj: export("ws-a", "wildwest", hashA, 1)})
	entry, _ = p.Get(hashA)
	require.Equal(t, ref("ws-b", "wildwest"), entry.Status.Owner)
	require.Empty(t, entry.Status.Holders)

	t.Log("deleting the last export removes the entry")
	p.OnDelete(export("ws-b", "wildwest", hashA, 2))
	_, ok = p.Get(hashA)
	require.False(t, ok)

	require.Equal(t, []string{"add a owner=ws-a", "update a owner=ws-a", "update a owner=ws-b", "delete a owner=ws-b"}, rec.events)
}

func TestProjectorListAndSelectors(t *testing.T) {
	t.Parallel()
	p := NewProjector(IsCoreExport)
	p.OnAdd(export("root", "tenancy.kcp.io", hashA, 1), true)
	p.OnAdd(export("org", "widgets", hashB, 1), true)

	names := func(entries []*adminv1alpha1.APIIdentity) []string {
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name)
		}
		return out
	}
	require.Equal(t, []string{hashA, hashB}, names(p.List(nil)))
	require.Equal(t, []string{hashA}, names(p.List(labels.SelectorFromSet(labels.Set{adminv1alpha1.APIIdentityCoreLabel: "true"}))))
	require.Equal(t, []string{hashB}, names(p.List(labels.SelectorFromSet(labels.Set{adminv1alpha1.APIIdentityOwnerLabel: "widgets"}))))
	require.Equal(t, []string{hashA}, names(p.List(labels.SelectorFromSet(labels.Set{adminv1alpha1.APIIdentityOwnerClusterLabel: "root"}))))

	t.Log("a late handler gets the current entries as initial adds")
	rec := &recorder{}
	p.AddEventHandler(rec)
	require.ElementsMatch(t, []string{"add a owner=root", "add b owner=org"}, rec.events)
}

func next(t *testing.T, w watch.Interface) watch.Event {
	t.Helper()
	select {
	case ev, ok := <-w.ResultChan():
		require.True(t, ok, "the watch closed")
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a watch event")
		return watch.Event{}
	}
}

func TestProjectorWatch(t *testing.T) {
	t.Parallel()

	t.Run("from now delivers only changes", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		p.OnAdd(export("ws-a", "wildwest", hashA, 1), true)
		w, err := p.Watch(nil, WatchOptions{})
		require.NoError(t, err)
		defer w.Stop()

		p.OnAdd(export("ws-b", "wildwest", hashA, 2), false)
		ev := next(t, w)
		require.Equal(t, watch.Modified, ev.Type)
		require.Len(t, ev.Object.(*adminv1alpha1.APIIdentity).Status.Holders, 1)
	})

	t.Run("watch-list sends the entries, a bookmark, then changes", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		p.OnAdd(export("ws-a", "wildwest", hashA, 1), true)
		w, err := p.Watch(nil, WatchOptions{SendInitialEvents: true, AllowWatchBookmarks: true})
		require.NoError(t, err)
		defer w.Stop()

		ev := next(t, w)
		require.Equal(t, watch.Added, ev.Type)
		ev = next(t, w)
		require.Equal(t, watch.Bookmark, ev.Type)
		require.Equal(t, "true", ev.Object.(*adminv1alpha1.APIIdentity).Annotations[metav1.InitialEventsAnnotationKey])
		require.Equal(t, "1", ev.Object.(*adminv1alpha1.APIIdentity).ResourceVersion)

		p.OnDelete(export("ws-a", "wildwest", hashA, 1))
		require.Equal(t, watch.Deleted, next(t, w).Type)
	})

	t.Run("resuming from a version replays what followed", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		p.OnAdd(export("ws-a", "wildwest", hashA, 1), true)
		rv := p.ResourceVersion()
		p.OnAdd(export("org", "widgets", hashB, 1), true)

		w, err := p.Watch(nil, WatchOptions{ResourceVersion: rv})
		require.NoError(t, err)
		defer w.Stop()
		ev := next(t, w)
		require.Equal(t, watch.Added, ev.Type)
		require.Equal(t, hashB, ev.Object.(*adminv1alpha1.APIIdentity).Name)
	})

	t.Run("a version from the future is expired", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		w, err := p.Watch(nil, WatchOptions{ResourceVersion: "42"})
		require.NoError(t, err)
		ev := next(t, w)
		require.Equal(t, watch.Error, ev.Type)
		require.True(t, apierrors.IsResourceExpired(apierrors.FromObject(ev.Object)))
		_, ok := <-w.ResultChan()
		require.False(t, ok, "the stream ends after the error")
	})

	t.Run("a version older than the history is expired", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		for range historyLength + 5 {
			p.OnAdd(export("ws-a", "wildwest", hashA, 1), true)
			p.OnDelete(export("ws-a", "wildwest", hashA, 1))
		}
		w, err := p.Watch(nil, WatchOptions{ResourceVersion: "1"})
		require.NoError(t, err)
		require.Equal(t, watch.Error, next(t, w).Type)
	})

	t.Run("a selector filters the stream", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		w, err := p.Watch(labels.SelectorFromSet(labels.Set{adminv1alpha1.APIIdentityCoreLabel: "true"}), WatchOptions{})
		require.NoError(t, err)
		defer w.Stop()

		p.OnAdd(export("org", "widgets", hashB, 1), false)
		p.OnAdd(export("root", "tenancy.kcp.io", hashA, 1), false)
		ev := next(t, w)
		require.Equal(t, hashA, ev.Object.(*adminv1alpha1.APIIdentity).Name)
	})

	t.Run("stop ends the stream", func(t *testing.T) {
		t.Parallel()
		p := NewProjector(IsCoreExport)
		w, err := p.Watch(nil, WatchOptions{})
		require.NoError(t, err)
		w.Stop()
		w.Stop()
		_, ok := <-w.ResultChan()
		require.False(t, ok)
	})
}
