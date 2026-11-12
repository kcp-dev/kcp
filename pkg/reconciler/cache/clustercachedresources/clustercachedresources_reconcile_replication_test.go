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

package clustercachedresources

import (
	"context"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	cachev1alpha1listers "github.com/kcp-dev/sdk/client/listers/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/informer"
)

type fakeGVRSource struct {
	lock    sync.Mutex
	gvrs    map[schema.GroupVersionResource]informer.GVRPartialMetadata
	changes chan struct{}
}

func newFakeGVRSource(gvrs map[schema.GroupVersionResource]informer.GVRPartialMetadata) *fakeGVRSource {
	return &fakeGVRSource{gvrs: gvrs, changes: make(chan struct{}, 1)}
}

func (s *fakeGVRSource) GVRs() map[schema.GroupVersionResource]informer.GVRPartialMetadata {
	s.lock.Lock()
	defer s.lock.Unlock()
	return maps.Clone(s.gvrs)
}

func (s *fakeGVRSource) Ready() bool {
	return true
}

func (s *fakeGVRSource) Subscribe() <-chan struct{} {
	return s.changes
}

func (s *fakeGVRSource) remove(gvr schema.GroupVersionResource) {
	s.lock.Lock()
	delete(s.gvrs, gvr)
	s.lock.Unlock()
	s.changes <- struct{}{}
}

func (s *fakeGVRSource) add(gvr schema.GroupVersionResource, metadata informer.GVRPartialMetadata) {
	s.lock.Lock()
	s.gvrs[gvr] = metadata
	s.lock.Unlock()
	s.changes <- struct{}{}
}

type fakeMetadataClient struct{}

func (c *fakeMetadataClient) Cluster(logicalcluster.Path) metadata.Interface { return nil }

func (c *fakeMetadataClient) Resource(gvr schema.GroupVersionResource) kcpmetadata.ResourceClusterInterface {
	return &fakeMetadataResourceClient{}
}

type fakeMetadataResourceClient struct{}

func (c *fakeMetadataResourceClient) Cluster(logicalcluster.Path) metadata.Getter { return nil }

func (c *fakeMetadataResourceClient) List(context.Context, metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return &metav1.PartialObjectMetadataList{ListMeta: metav1.ListMeta{ResourceVersion: "1"}}, nil
}

func (c *fakeMetadataResourceClient) Watch(_ context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	if opts.SendInitialEvents != nil && *opts.SendInitialEvents {
		return nil, apierrors.NewBadRequest("watch-list not supported")
	}
	return watch.NewFake(), nil
}

var (
	widgetsGVR      = schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1alpha1", Resource: "widgets"}
	widgetsMetadata = informer.GVRPartialMetadata{Scope: apiextensionsv1.ClusterScoped, Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "Widget", Singular: "widget"}}
)

type testReplication struct {
	*replication
	localSource *fakeGVRSource
	stopWorker  context.CancelFunc
	ccr         *cachev1alpha1.ClusterCachedResource
	requeues    atomic.Int32
}

// newTestReplication runs the local factory worker like the server does. The global
// factory has no worker, same as CachePartialMetadataDDSIF.
func newTestReplication(t *testing.T) *testReplication {
	t.Helper()

	newSource := func() *fakeGVRSource {
		return newFakeGVRSource(map[schema.GroupVersionResource]informer.GVRPartialMetadata{widgetsGVR: widgetsMetadata})
	}
	newFactory := func(source *fakeGVRSource) *informer.DiscoveringDynamicSharedInformerFactory {
		f, err := informer.NewDiscoveringPartialMetadataSharedInformerFactory(&fakeMetadataClient{}, func(any) bool { return true }, func(any) bool { return false }, nil, source, cache.Indexers{})
		require.NoError(t, err)
		return f
	}

	tr := &testReplication{localSource: newSource()}
	local := newFactory(tr.localSource)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tr.stopWorker = cancel
	go local.StartWorker(ctx)

	tr.replication = &replication{
		shardName: "root",
		getKind: func(logicalcluster.Name, schema.GroupVersionResource) (schema.GroupVersionKind, error) {
			return widgetsGVR.GroupVersion().WithKind("Widget"), nil
		},
		localDiscoveringDynamicKcpInformers:  local,
		globalDiscoveringDynamicKcpInformers: newFactory(newSource()),
		requeueSelf:                          func(any) { tr.requeues.Add(1) },
		controllerRegistry:                   newRegistry(),
	}
	tr.ccr = &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{Group: widgetsGVR.Group, Version: widgetsGVR.Version, Resource: widgetsGVR.Resource},
		},
	}
	require.Eventually(t, func() bool { return tr.servedLocally(widgetsGVR) }, wait.ForeverTestTimeout, 100*time.Millisecond)
	return tr
}

func (tr *testReplication) started() bool {
	c := tr.controllerRegistry.get(controllerNameFor(tr.ccr))
	return c != nil && c.Started()
}

func (tr *testReplication) registered() bool {
	return tr.controllerRegistry.get(controllerNameFor(tr.ccr)) != nil
}

// Informers must keep running when the leader election context is cancelled.
func TestReplicationInformersSurviveLeaderElection(t *testing.T) {
	t.Parallel()
	tr := newTestReplication(t)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := tr.reconcile(ctx, tr.ccr)
	require.NoError(t, err)
	require.Eventually(t, tr.started, wait.ForeverTestTimeout, 100*time.Millisecond)

	cancel()
	require.Eventually(t, func() bool { return !tr.registered() }, wait.ForeverTestTimeout, 100*time.Millisecond)

	l, err := tr.localDiscoveringDynamicKcpInformers.ForResource(widgetsGVR)
	require.NoError(t, err)
	require.False(t, l.Informer().IsStopped(), "local informer should not be stopped")
	g, err := tr.globalDiscoveringDynamicKcpInformers.ForResource(widgetsGVR)
	require.NoError(t, err)
	require.False(t, g.Informer().IsStopped(), "global informer should not be stopped")

	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err = tr.reconcile(ctx, tr.ccr)
	require.NoError(t, err)
	require.Eventually(t, tr.started, wait.ForeverTestTimeout, 100*time.Millisecond, "replication should start again")
}

// When the CRD is removed the controller must stop and requeue, and must not be
// recreated on top of an informer the local factory doesn't know about.
func TestReplicationStopsWhenCRDIsRemoved(t *testing.T) {
	t.Parallel()
	tr := newTestReplication(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err := tr.reconcile(ctx, tr.ccr)
	require.NoError(t, err)
	require.Eventually(t, tr.started, wait.ForeverTestTimeout, 100*time.Millisecond)
	// Start requeues once when it is running.
	require.Eventually(t, func() bool { return tr.requeues.Load() == 1 }, wait.ForeverTestTimeout, 100*time.Millisecond)

	tr.localSource.remove(widgetsGVR)
	require.Eventually(t, func() bool { return !tr.registered() }, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.Eventually(t, func() bool { return tr.requeues.Load() == 2 }, wait.ForeverTestTimeout, 100*time.Millisecond)

	// getKind still returns the kind, like a REST mapper which didn't catch up yet.
	_, err = tr.reconcile(ctx, tr.ccr)
	require.Error(t, err)
	require.False(t, tr.registered())
	informers, notSynced := tr.localDiscoveringDynamicKcpInformers.Informers()
	require.NotContains(t, informers, widgetsGVR)
	require.NotContains(t, notSynced, widgetsGVR)
}

// A stopped informer must fail the reconcile, so it is retried with backoff instead of
// requeueing itself in a loop.
func TestReplicationWithStoppedInformer(t *testing.T) {
	t.Parallel()
	tr := newTestReplication(t)

	l, err := tr.localDiscoveringDynamicKcpInformers.ForResource(widgetsGVR)
	require.NoError(t, err)
	tr.stopWorker()
	require.Eventually(t, l.Informer().IsStopped, wait.ForeverTestTimeout, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err = tr.reconcile(ctx, tr.ccr)
	require.Error(t, err)
	require.False(t, tr.registered())
}

// When the CRD comes back, ClusterCachedResources failing in backoff must be retried right away.
func TestEnqueueWhenCRDComesBack(t *testing.T) {
	t.Parallel()
	tr := newTestReplication(t)

	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
	require.NoError(t, indexer.Add(tr.ccr))
	c := &Controller{
		// long backoff, so the key is only in the queue if it was added
		queue:                               workqueue.NewTypedRateLimitingQueue(workqueue.NewTypedItemExponentialFailureRateLimiter[string](time.Hour, time.Hour)),
		ClusterCachedResourceLister:         cachev1alpha1listers.NewClusterCachedResourceClusterLister(indexer),
		localDiscoveringDynamicKcpInformers: tr.localDiscoveringDynamicKcpInformers,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Start(ctx, 0)

	key, err := kcpcache.MetaClusterNamespaceKeyFunc(tr.ccr)
	require.NoError(t, err)

	tr.localSource.remove(widgetsGVR)
	require.Eventually(t, func() bool { return !tr.servedLocally(widgetsGVR) }, wait.ForeverTestTimeout, 100*time.Millisecond)
	// reconcile failing while the CRD is gone
	c.queue.AddRateLimited(key)
	require.Equal(t, 1, c.queue.NumRequeues(key))
	require.Equal(t, 0, c.queue.Len())

	tr.localSource.add(widgetsGVR, widgetsMetadata)
	require.Eventually(t, func() bool {
		return c.queue.NumRequeues(key) == 0 && c.queue.Len() == 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
