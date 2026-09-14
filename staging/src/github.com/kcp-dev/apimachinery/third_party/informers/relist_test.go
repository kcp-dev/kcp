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

package informers_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/apimachinery/v2/third_party/informers"
	"github.com/kcp-dev/logicalcluster/v3"
)

// fakeListerWatcher serves a mutable set of objects. It supports both the
// classic LIST+WATCH protocol and the streaming watch-list protocol
// (sendInitialEvents=true), so the reflector can be exercised in the mode
// it runs in production.
type fakeListerWatcher struct {
	t    *testing.T
	mu   sync.Mutex
	rv   int
	objs map[string]*metav1.PartialObjectMetadata
}

func (f *fakeListerWatcher) List(_ metav1.ListOptions) (runtime.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	list := &metav1.PartialObjectMetadataList{}
	list.ResourceVersion = strconv.Itoa(f.rv)
	for _, o := range f.objs {
		list.Items = append(list.Items, *o.DeepCopy())
	}
	return list, nil
}

func (f *fakeListerWatcher) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	w := watch.NewRaceFreeFake()
	if opts.SendInitialEvents != nil && *opts.SendInitialEvents {
		for _, o := range f.objs {
			w.Add(o.DeepCopy())
		}
		bookmark := &metav1.PartialObjectMetadata{}
		bookmark.ResourceVersion = strconv.Itoa(f.rv)
		bookmark.Annotations = map[string]string{metav1.InitialEventsAnnotationKey: "true"}
		w.Action(watch.Bookmark, bookmark)
	}
	return w, nil
}

func (f *fakeListerWatcher) add(cluster, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rv++
	o := &metav1.PartialObjectMetadata{}
	o.Name = name
	o.Namespace = "default"
	o.UID = types.UID(cluster + "-" + name)
	o.ResourceVersion = strconv.Itoa(f.rv)
	o.Annotations = map[string]string{logicalcluster.AnnotationKey: cluster}
	f.objs[cluster+"|"+name] = o
}

// deleteWithoutEvent removes the object server-side without emitting a
// watch event, i.e. the DELETED event is lost because the watch was torn
// down at the same time. Only a relist can surface this deletion.
func (f *fakeListerWatcher) deleteWithoutEvent(cluster, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rv++
	delete(f.objs, cluster+"|"+name)
}

// TestForceRelistSurfacesMissedDeletes verifies that a relist removes objects
// from the store that disappeared while no watch was active, and that event
// handlers are notified about them via a DeletedFinalStateUnknown tombstone.
//
// This regressed silently because the indexer was keyed with a key function
// that cannot handle DeletedFinalStateUnknown: the synthetic delete produced
// by the relist failed with "object has no meta", was dropped, and the stale
// object stayed in the store forever.
func TestForceRelistSurfacesMissedDeletes(t *testing.T) {
	for _, watchList := range []bool{true, false} {
		t.Run("watchList="+strconv.FormatBool(watchList), func(t *testing.T) {
			clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, watchList)
			testForceRelistSurfacesMissedDeletes(t)
		})
	}
}

func testForceRelistSurfacesMissedDeletes(t *testing.T) {
	lw := &fakeListerWatcher{t: t, objs: map[string]*metav1.PartialObjectMetadata{}}
	lw.add("cluster1", "owner")
	lw.add("cluster2", "owner")

	inf := informers.NewSharedIndexInformer(
		&cache.ListWatch{ListFunc: lw.List, WatchFunc: lw.Watch},
		&metav1.PartialObjectMetadata{},
		0,
		cache.Indexers{},
	)

	var mu sync.Mutex
	adds := map[string]int{}
	deletes := map[string]int{}
	tombstones := map[string]int{}
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			defer mu.Unlock()
			adds[objKey(t, obj)]++
		},
		DeleteFunc: func(obj interface{}) {
			mu.Lock()
			defer mu.Unlock()
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				tombstones[d.Key]++
				obj = d.Obj
			}
			deletes[objKey(t, obj)]++
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go inf.Run(ctx.Done())
	require.True(t, cache.WaitForCacheSync(ctx.Done(), inf.HasSynced))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return adds["cluster1|default/owner"] == 1 && adds["cluster2|default/owner"] == 1
	}, 5*time.Second, 10*time.Millisecond, "expected initial adds")

	lw.deleteWithoutEvent("cluster1", "owner")
	inf.ForceRelist()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return deletes["cluster1|default/owner"] == 1
	}, 10*time.Second, 50*time.Millisecond, "expected relist to surface the missed delete, store keys: %v", inf.GetStore().ListKeys())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, tombstones["cluster1|default/owner"], "expected the delete to be delivered as a tombstone")
	require.Zero(t, deletes["cluster2|default/owner"], "object in other cluster must not be deleted")
	require.ElementsMatch(t, []string{"cluster2|default/owner"}, inf.GetStore().ListKeys())
}

func objKey(t *testing.T, obj interface{}) string {
	t.Helper()
	o, ok := obj.(*metav1.PartialObjectMetadata)
	require.True(t, ok, "unexpected object type %T", obj)
	return logicalcluster.From(o).String() + "|" + o.Namespace + "/" + o.Name
}
