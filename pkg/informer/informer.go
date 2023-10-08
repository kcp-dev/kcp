/*
Copyright 2021 The KCP Authors.

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

package informer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	kcpinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/projection"
)

const (
	resyncPeriod = 10 * time.Hour

	byGroupVersionResourceIndex = "byGroupVersionResource"
)

// GVRPartialMetadata provides the required metadata about a GVR
// for which an informer should be started.
type GVRPartialMetadata struct {
	Names apiextensionsv1.CustomResourceDefinitionNames
	Scope apiextensionsv1.ResourceScope
}

// GVRSource is a source of information about available GVRs,
// and provides a way to register for changes in the available GVRs (addition or removal)
// so that the list of informers can be updated.
type GVRSource interface {
	// GVRs returns the required metadata about all GVRs known by the GVRSource
	GVRs() map[schema.GroupVersionResource]GVRPartialMetadata

	// Ready returns true if the GVRSource is ready to return available GVRs.
	// For informer-based GVRSources (watching CRDs for example), it would return true if the underlying
	// informer is synced.
	Ready() bool

	// Subscribe returns a new channel to which the GVRSource writes whenever
	// its list of known GVRs has changed
	Subscribe() <-chan struct{}
}

// genericInformerBase is a base interface that would be satisfied by both
// cluster-aware and cluster-unaware generic informers.
type genericInformerBase[Informer cache.SharedIndexInformer, Lister genericListerBase] interface {
	// Informer is the shared index informer returned by the generic informer.
	// It would be either a SharedIndexInformer or a ScopeableSharedIndexInformer,
	// according to the cluster-awareness.
	Informer() Informer

	// Lister is the generic lister returned by the generic informer.
	// It would be either a GenericLister or a GenericClusterLister,
	// according to the cluster-awareness
	Lister() Lister
}

type genericListerBase interface {
	List(selector labels.Selector) (ret []runtime.Object, err error)
}

var _ genericInformerBase[kcpcache.ScopeableSharedIndexInformer, kcpcache.GenericClusterLister] = (kcpinformers.GenericClusterInformer)(nil)
var _ genericInformerBase[cache.SharedIndexInformer, cache.GenericLister] = (informers.GenericInformer)(nil)

// GenericDiscoveringDynamicSharedInformerFactory is a SharedInformerFactory that
// dynamically discovers new types and begins informing on them.
// It is a generic implementation for both logical-cluster-aware and
// logical-cluster-unaware (== single cluster, standard kube) informers.
type GenericDiscoveringDynamicSharedInformerFactory[Informer cache.SharedIndexInformer, Lister genericListerBase, GenericInformer genericInformerBase[Informer, Lister]] struct {
	newInformer func(gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers) GenericInformer
	filterFunc  func(interface{}) bool
	indexers    cache.Indexers
	gvrSource   GVRSource

	// handlersLock protects multiple writers racing to update handlers.
	handlersLock sync.Mutex
	handlers     atomic.Value

	// updateCh receives notifications for all CRD add/update/delete events, so we can start new informers and stop
	// informers no longer needed.
	updateCh chan struct{}

	informersLock    sync.RWMutex
	informers        map[schema.GroupVersionResource]GenericInformer
	startedInformers map[schema.GroupVersionResource]bool
	informerStops    map[schema.GroupVersionResource]chan struct{}
	discoveryData    discoveryData
	restMapper       restMapper

	// Support subscribers (e.g. quota) that want to know when informers/discovery have changed.
	subscribersLock sync.Mutex
	subscribers     map[string]chan<- struct{}
}

// NewGenericDiscoveringDynamicSharedInformerFactory returns is an informer factory that
// dynamically discovers new types and begins informing on them.
// It is a generic implementation for both logical-cluster-aware and
// logical-cluster-unaware (== single cluster, standard kube) informers.
func NewGenericDiscoveringDynamicSharedInformerFactory[Informer cache.SharedIndexInformer, Lister genericListerBase, GenericInformer genericInformerBase[Informer, Lister]](
	newInformer func(gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers) GenericInformer,
	filterFunc func(obj interface{}) bool,
	gvrSource GVRSource,
	indexers cache.Indexers,
) (*GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer], error) {
	if filterFunc == nil {
		filterFunc = func(obj interface{}) bool { return true }
	}

	f := &GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]{
		filterFunc: filterFunc,
		indexers:   indexers,
		gvrSource:  gvrSource,

		newInformer: newInformer,

		// Use a buffered channel of size 1 to allow enqueuing 1 update notification
		updateCh: make(chan struct{}, 1),

		informers:        make(map[schema.GroupVersionResource]GenericInformer),
		startedInformers: make(map[schema.GroupVersionResource]bool),
		informerStops:    make(map[schema.GroupVersionResource]chan struct{}),

		subscribers: make(map[string]chan<- struct{}),
	}

	f.restMapper = newRESTMapper(func() (meta.RESTMapper, error) {
		return restmapper.NewDiscoveryRESTMapper(f.discoveryData.apiGroupResources), nil
	})

	f.handlers.Store([]GVREventHandler{})

	changes := gvrSource.Subscribe()
	go func() {
		for change := range changes {
			select {
			case f.updateCh <- change:
				klog.Background().V(4).Info("enqueued update notification for dynamic informer recalculation")
			default:
				klog.Background().V(5).Info("dropping update notification for dynamic informer recalculation because a notification is already pending")
			}
		}
	}()

	return f, nil
}

// NewScopedDiscoveringDynamicSharedInformerFactory returns a factory for
// cluster-unaware (standard Kube) shared informers that discovers new types
// and informs on updates to resources of those types.
// It receives the GVR-related information by delegating to a GVRSource.
func NewScopedDiscoveringDynamicSharedInformerFactory(
	dynamicClient dynamic.Interface,
	filterFunc func(obj interface{}) bool,
	tweakListOptions dynamicinformer.TweakListOptionsFunc,
	gvrSource GVRSource,
	indexers cache.Indexers,
) (*GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer], error) {
	if filterFunc == nil {
		filterFunc = func(obj interface{}) bool { return true }
	}

	return NewGenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister](
		func(gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers) informers.GenericInformer {
			return dynamicinformer.NewFilteredDynamicInformer(
				dynamicClient,
				gvr,
				"",
				resyncPeriod,
				indexers,
				tweakListOptions,
			)
		},
		filterFunc,
		gvrSource,
		indexers,
	)
}

// NewDiscoveringDynamicSharedInformerFactory returns a factory for cluster-aware
// shared informers that discovers new types and informs on updates to resources of
// those types.
// It receives the GVR-related information by delegating to a GVRSource.
func NewDiscoveringDynamicSharedInformerFactory(
	dynamicClusterClient kcpdynamic.ClusterInterface,
	filterFunc func(obj interface{}) bool,
	tweakListOptions dynamicinformer.TweakListOptionsFunc,
	gvrSource GVRSource,
	indexers cache.Indexers,
) (*DiscoveringDynamicSharedInformerFactory, error) {
	f, err := NewGenericDiscoveringDynamicSharedInformerFactory[kcpcache.ScopeableSharedIndexInformer, kcpcache.GenericClusterLister](
		func(gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers) kcpinformers.GenericClusterInformer {
			indexers[kcpcache.ClusterIndexName] = kcpcache.ClusterIndexFunc
			indexers[kcpcache.ClusterAndNamespaceIndexName] = kcpcache.ClusterAndNamespaceIndexFunc
			return kcpdynamicinformer.NewFilteredDynamicInformer(
				dynamicClusterClient,
				gvr,
				resyncPeriod,
				indexers,
				tweakListOptions,
			)
		},
		filterFunc,
		gvrSource,
		indexers,
	)

	return &DiscoveringDynamicSharedInformerFactory{
		GenericDiscoveringDynamicSharedInformerFactory: f,
	}, err
}

// DiscoveringDynamicSharedInformerFactory is a factory for cluster-aware
// shared informers that discovers new types and informs on updates to resources of
// those types.
// It offers an additional `Cluster()` method that returns a new shared informer factory
// based on the main informer factory, but scoped for a given logical cluster.
type DiscoveringDynamicSharedInformerFactory struct {
	*GenericDiscoveringDynamicSharedInformerFactory[kcpcache.ScopeableSharedIndexInformer, kcpcache.GenericClusterLister, kcpinformers.GenericClusterInformer]
}

func (d *DiscoveringDynamicSharedInformerFactory) Cluster(cluster logicalcluster.Name) kcpinformers.ScopedDynamicSharedInformerFactory {
	return &scopedDiscoveringDynamicSharedInformerFactory{
		DiscoveringDynamicSharedInformerFactory: d,
		cluster:                                 cluster,
	}
}

type scopedDiscoveringDynamicSharedInformerFactory struct {
	*DiscoveringDynamicSharedInformerFactory
	cluster logicalcluster.Name
}

// ForResource returns the GenericInformer for gvr, creating it if needed. The GenericInformer must be started
// by calling Start on the DiscoveringDynamicSharedInformerFactory before the GenericInformer can be used.
func (d *scopedDiscoveringDynamicSharedInformerFactory) ForResource(gvr schema.GroupVersionResource) (informers.GenericInformer, error) {
	clusterInformer, err := d.DiscoveringDynamicSharedInformerFactory.ForResource(gvr)
	if err != nil {
		return nil, err
	}
	return clusterInformer.Cluster(d.cluster), nil
}

// ForResource returns the GenericInformer for gvr, creating it if needed. The GenericInformer must be started
// by calling Start on the GenericDiscoveringDynamicSharedInformerFactory before the GenericInformer can be used.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) ForResource(gvr schema.GroupVersionResource) (GenericInformer, error) {
	// See if we already have it
	d.informersLock.RLock()
	inf := d.informers[gvr]
	d.informersLock.RUnlock()

	if (genericInformerBase[Informer, Lister])(inf) != nil {
		return inf, nil
	}

	// Grab the write lock, then find-or-create
	d.informersLock.Lock()
	defer d.informersLock.Unlock()

	return d.informerForResourceLockHeld(gvr), nil
}

// informerForResourceLockHeld returns the GenericInformer for gvr, creating it if needed. The caller must have the write
// lock before calling this method.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) informerForResourceLockHeld(gvr schema.GroupVersionResource) GenericInformer {
	// In case it was created in between the initial check while the rlock was held and when the write lock was
	// acquired, return it instead of creating a 2nd copy and overwriting.
	inf := d.informers[gvr]
	if (genericInformerBase[Informer, Lister])(inf) != nil {
		return inf
	}

	klog.Background().V(2).WithValues("gvr", gvr).Info("adding dynamic informer for gvr")

	indexers := cache.Indexers{}
	for k, v := range d.indexers {
		if k == cache.NamespaceIndex {
			// Don't allow overriding NamespaceIndex
			continue
		}

		indexers[k] = v
	}

	// Definitely need to create it
	inf = d.newInformer(gvr, resyncPeriod, indexers)

	_, _ = inf.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: d.filterFunc,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				for _, h := range d.handlers.Load().([]GVREventHandler) {
					h.OnAdd(gvr, obj)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				for _, h := range d.handlers.Load().([]GVREventHandler) {
					h.OnUpdate(gvr, oldObj, newObj)
				}
			},
			DeleteFunc: func(obj interface{}) {
				for _, h := range d.handlers.Load().([]GVREventHandler) {
					h.OnDelete(gvr, obj)
				}
			},
		},
	})

	// Store in cache
	d.informers[gvr] = inf

	return inf
}

// Informers returns a map of per-resource-type generic informers for all types that are
// known by this informer factory, and that are synced.
//
// If any informers aren't synced, their GVRs are returned so that they can be
// checked and processed later.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) Informers() (informers map[schema.GroupVersionResource]GenericInformer, notSynced []schema.GroupVersionResource) {
	informers = map[schema.GroupVersionResource]GenericInformer{}

	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	for gvr, informer := range d.informers {
		// We have the read lock so d.informers is fully populated for all the gvrs in d.gvrs. We use d.informers
		// directly instead of calling either ForResource or informerForResourceLockHeld.
		if !informer.Informer().HasSynced() {
			notSynced = append(notSynced, gvr)
			continue
		}

		informers[gvr] = informer
	}

	return informers, notSynced
}

// GVREventHandler is an event handler that includes the GroupVersionResource
// of the resource being handled.
type GVREventHandler interface {
	OnAdd(gvr schema.GroupVersionResource, obj interface{})
	OnUpdate(gvr schema.GroupVersionResource, oldObj, newObj interface{})
	OnDelete(gvr schema.GroupVersionResource, obj interface{})
}

type GVREventHandlerFuncs struct {
	AddFunc    func(gvr schema.GroupVersionResource, obj interface{})
	UpdateFunc func(gvr schema.GroupVersionResource, oldObj, newObj interface{})
	DeleteFunc func(gvr schema.GroupVersionResource, obj interface{})
}

func (g GVREventHandlerFuncs) OnAdd(gvr schema.GroupVersionResource, obj interface{}) {
	if g.AddFunc != nil {
		g.AddFunc(gvr, obj)
	}
}
func (g GVREventHandlerFuncs) OnUpdate(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
	if g.UpdateFunc != nil {
		g.UpdateFunc(gvr, oldObj, newObj)
	}
}
func (g GVREventHandlerFuncs) OnDelete(gvr schema.GroupVersionResource, obj interface{}) {
	if g.DeleteFunc != nil {
		g.DeleteFunc(gvr, obj)
	}
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) AddEventHandler(handler GVREventHandler) {
	d.handlersLock.Lock()

	handlers := d.handlers.Load().([]GVREventHandler)

	newHandlers := make([]GVREventHandler, len(handlers))
	copy(newHandlers, handlers)

	newHandlers = append(newHandlers, handler)

	d.handlers.Store(newHandlers)

	d.handlersLock.Unlock()
}

// StartWorker starts the worker that waits for notifications that informer updates are needed. This call is blocking,
// stopping when ctx.Done() is closed.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) StartWorker(ctx context.Context) {
	logger := klog.FromContext(ctx)
	defer func() {
		d.informersLock.Lock()

		for _, stopCh := range d.informerStops {
			close(stopCh)
		}

		d.informersLock.Unlock()
	}()

	if !cache.WaitForNamedCacheSync("kcp-ddsif-gvr-source", ctx.Done(), d.gvrSource.Ready) {
		logger.Error(nil, "GVR source never synced")
		return
	}

	// Now that the CRD informer has synced, do an initial update
	d.updateInformers()

	// Use UntilWithContext here so that we only check updateCh at most once every second. Because a flurry of several
	// watch events for CRDs can come in quickly, this effectively "batches" them, so we aren't recalculating the
	// informers for each watch event in a tightly grouped set of events.
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		logger.V(5).Info("waiting for notification")
		select {
		case <-ctx.Done():
			return
		case <-d.updateCh:
		}

		logger.V(5).Info("notification received")
		d.updateInformers()
	}, time.Second)
}

func gvrFor(group, version, resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}
}

func withGVRPartialMetadata(scope apiextensionsv1.ResourceScope, kind, singular string) GVRPartialMetadata {
	return GVRPartialMetadata{
		Scope: scope,
		Names: apiextensionsv1.CustomResourceDefinitionNames{
			Kind:     kind,
			Singular: singular,
		},
	}
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) updateInformers() {
	logger := klog.Background()
	logger.V(5).Info("determining dynamic informer additions and removals")

	// Get the unique set of Group(Version)Resources (version doesn't matter because we're expecting a wildcard
	// partial metadata client, but we need a version in the request, so we need it here) and add them to latest.
	crdGVRs := d.gvrSource.GVRs()
	latest := make(map[schema.GroupVersionResource]GVRPartialMetadata, len(crdGVRs))
	for gvr, gvrMetadata := range crdGVRs {
		// Don't start a dynamic informer for projected resources.
		// Starting an informer for them causes problems when the virtual-workspaces server is deployed
		// separately. See https://github.com/kcp-dev/kcp/issues/1654 for more details.
		if projection.Includes(gvr) {
			continue
		}

		latest[gvr] = gvrMetadata
	}

	// Grab a read lock to compare against d.informers to see if we need to start or stop any informers
	d.informersLock.RLock()
	informersToAdd, informersToRemove := d.calculateInformersLockHeld(latest)
	d.informersLock.RUnlock()

	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		logger.V(5).Info("no changes")
		return
	}

	// We have to add/remove, so we need the write lock
	d.informersLock.Lock()
	defer d.informersLock.Unlock()

	// Recalculate in case another goroutine did this work in between when we had the read lock and when we acquired
	// the write lock
	informersToAdd, informersToRemove = d.calculateInformersLockHeld(latest)
	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		logger.V(5).Info("no changes")
		return
	}

	// Now we definitely need to do this work
	for i := range informersToAdd {
		gvr := informersToAdd[i]

		// We have the write lock, so call the LH variant
		inf := d.informerForResourceLockHeld(gvr)

		// Set up a stop channel for this specific informer
		stop := make(chan struct{})
		go inf.Informer().Run(stop)

		// And store it
		d.informerStops[gvr] = stop
		d.startedInformers[gvr] = true
	}

	for i := range informersToRemove {
		gvr := informersToRemove[i]
		logger := logger.WithValues("gvr", gvr)

		logger.V(2).Info("removing dynamic informer for gvr")

		stop, ok := d.informerStops[gvr]
		if ok {
			logger.V(4).Info("closing stop channel for dynamic informer for gvr")
			close(stop)
		}

		logger.V(4).Info("removing dynamic informer from maps for gvr")
		delete(d.informers, gvr)
		delete(d.informerStops, gvr)
		delete(d.startedInformers, gvr)
	}

	d.discoveryData = gvrsToDiscoveryData(latest)
	d.restMapper = newRESTMapper(func() (meta.RESTMapper, error) {
		return restmapper.NewDiscoveryRESTMapper(d.discoveryData.apiGroupResources), nil
	})

	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	for id, ch := range d.subscribers {
		logger := logger.WithValues("id", id)
		logger.V(4).Info("attempting to notify discovery subscriber")
		select {
		case ch <- struct{}{}:
			logger.V(4).Info("successfully notified discovery subscriber")
		default:
			logger.V(4).Info("unable to notify discovery subscriber - channel full")
		}
	}
}

// gvrsToDiscoveryData returns discovery data for all the resources covered by the factory. It only
// includes enough data in each APIResource to support what kcp currently needs (scheduling, placement, quota, GC).
func gvrsToDiscoveryData(gvrs map[schema.GroupVersionResource]GVRPartialMetadata) discoveryData {
	ret := discoveryData{
		apiGroupResources: make([]*restmapper.APIGroupResources, 0),
		apiResourceList:   make([]*metav1.APIResourceList, 0),
	}

	gvResources := make(map[string]map[string][]metav1.APIResource)

	for gvr, metadata := range gvrs {
		apiResource := metav1.APIResource{
			Name:         gvr.Resource,
			Group:        gvr.Group,
			Version:      gvr.Version,
			Kind:         metadata.Names.Kind,
			SingularName: metadata.Names.Singular,
			Namespaced:   metadata.Scope == apiextensionsv1.NamespaceScoped,
			// Everything we're informing on supports these
			Verbs: []string{"create", "list", "watch", "delete"},
		}
		if gvResources[gvr.Group] == nil {
			gvResources[gvr.Group] = make(map[string][]metav1.APIResource)
		}
		gvResources[gvr.Group][gvr.Version] = append(gvResources[gvr.Group][gvr.Version], apiResource)
	}

	for group, resources := range gvResources {
		var versions []metav1.GroupVersionForDiscovery
		versionedResources := make(map[string][]metav1.APIResource)

		for version, apiResource := range resources {
			versions = append(versions, metav1.GroupVersionForDiscovery{GroupVersion: group, Version: version})

			sort.Slice(apiResource, func(i, j int) bool {
				return apiResource[i].Name < apiResource[j].Name
			})

			versionedResources[version] = apiResource

			apiResourceList := &metav1.APIResourceList{
				GroupVersion: metav1.GroupVersion{Group: group, Version: version}.String(),
				APIResources: apiResource,
			}

			ret.apiResourceList = append(ret.apiResourceList, apiResourceList)
		}
		apiGroup := metav1.APIGroup{
			Name:     group,
			Versions: versions,
			// We may want to fill the PreferredVersion based on the storage version,
			// though it's not currently required by the kcp controllers that rely on
			// the discovery data provided by the dynamic shared informer factory, e.g.,
			// the quota and garbage collector controllers.
		}

		ret.apiGroupResources = append(ret.apiGroupResources, &restmapper.APIGroupResources{
			Group:              apiGroup,
			VersionedResources: versionedResources,
		})
	}

	sort.Slice(ret.apiGroupResources, func(i, j int) bool {
		return ret.apiGroupResources[i].Group.Name < ret.apiGroupResources[j].Group.Name
	})

	sort.Slice(ret.apiResourceList, func(i, j int) bool {
		return ret.apiResourceList[i].GroupVersion < ret.apiResourceList[j].GroupVersion
	})

	return ret
}

// Start starts any informers that have been created but not yet started. The passed in stop channel is ignored;
// instead, a new stop channel is created, so the factory can properly stop the informer if/when the API is removed.
// Like other shared informer factories, this call is non-blocking.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) Start(_ <-chan struct{}) {
	d.informersLock.Lock()
	defer d.informersLock.Unlock()

	for gvr, informer := range d.informers {
		if !d.startedInformers[gvr] {
			// Set up a stop channel for this specific informer
			stop := make(chan struct{})
			go informer.Informer().Run(stop)

			// And store it
			d.informerStops[gvr] = stop
			d.startedInformers[gvr] = true
		}
	}
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) calculateInformersLockHeld(latest map[schema.GroupVersionResource]GVRPartialMetadata) (toAdd, toRemove []schema.GroupVersionResource) {
	for gvr := range latest {
		if _, found := d.informers[gvr]; !found {
			toAdd = append(toAdd, gvr)
		}
	}

	for gvr := range d.informers {
		if _, found := latest[gvr]; !found {
			toRemove = append(toRemove, gvr)
		}
	}

	return toAdd, toRemove
}

// Subscribe registers for informer/discovery change notifications, returning a channel to which change notifications
// are sent.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) Subscribe(id string) <-chan struct{} {
	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	// Use a buffered channel so we can always send at least 1, regardless of consumer status.
	ch := make(chan struct{}, 1)
	d.subscribers[id] = ch

	return ch
}

// Unsubscribe removes the channel associated with id from future informer/discovery change notifications.
func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) Unsubscribe(id string) {
	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	ch, ok := d.subscribers[id]
	if ok {
		close(ch)
	}

	delete(d.subscribers, id)
}

type discoveryData struct {
	apiGroupResources []*restmapper.APIGroupResources
	apiResourceList   []*metav1.APIResourceList
}

var _ discovery.ServerResourcesInterface = &GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, genericListerBase, genericInformerBase[cache.SharedIndexInformer, genericListerBase]]{}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	for _, apiResourceList := range d.discoveryData.apiResourceList {
		if apiResourceList.GroupVersion == groupVersion {
			return apiResourceList.DeepCopy(), nil
		}
	}

	// ignore 403 or 404 error to be compatible with a v1.0 server.
	if groupVersion == "v1" {
		return &metav1.APIResourceList{GroupVersion: groupVersion}, nil
	}

	return nil, errors.NewNotFound(schema.GroupResource{Group: groupVersion}, "")
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	retGroups := make([]*metav1.APIGroup, len(d.discoveryData.apiGroupResources))
	for i, apiGroupResources := range d.discoveryData.apiGroupResources {
		retGroups[i] = apiGroupResources.Group.DeepCopy()
	}

	retResourceList := make([]*metav1.APIResourceList, len(d.discoveryData.apiResourceList))
	for i, apiResourceList := range d.discoveryData.apiResourceList {
		retResourceList[i] = apiResourceList.DeepCopy()
	}

	return retGroups, retResourceList, nil
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	ret := make([]*metav1.APIResourceList, len(d.discoveryData.apiResourceList))
	for i, apiResourceList := range d.discoveryData.apiResourceList {
		ret[i] = apiResourceList.DeepCopy()
	}

	return ret, nil
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	ret := make([]*metav1.APIResourceList, len(d.discoveryData.apiResourceList))
	for i, apiResourceList := range d.discoveryData.apiResourceList {
		namespacedResources := &metav1.APIResourceList{GroupVersion: apiResourceList.GroupVersion}
		for _, resource := range apiResourceList.APIResources {
			if resource.Namespaced {
				namespacedResources.APIResources = append(namespacedResources.APIResources, resource)
			}
		}
		ret[i] = namespacedResources
	}

	return ret, nil
}

func (d *GenericDiscoveringDynamicSharedInformerFactory[Informer, Lister, GenericInformer]) RESTMapper() meta.ResettableRESTMapper {
	return &d.restMapper
}

func newRESTMapper(fn func() (meta.RESTMapper, error)) restMapper {
	return restMapper{
		meta.NewLazyRESTMapperLoader(fn),
	}
}

// NewCRDGVRSource returns a GVRSource based on CRDs watched in the given informer.
func NewCRDGVRSource(informer cache.SharedIndexInformer) (*crdGVRSource, error) {
	// Add an index function that indexes a CRD by its group/version/resource.
	if err := informer.AddIndexers(cache.Indexers{
		byGroupVersionResourceIndex: byGroupVersionResourceIndexFunc,
	}); err != nil {
		return nil, err
	}

	return &crdGVRSource{
		crdInformer: informer,
		crdIndexer:  informer.GetIndexer(),
	}, nil
}

func byGroupVersionResourceKeyFunc(group, version, resource string) string {
	return fmt.Sprintf("%s/%s/%s", group, version, resource)
}

func byGroupVersionResourceIndexFunc(obj interface{}) ([]string, error) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil, fmt.Errorf("%T is not a CustomResourceDefinition", obj)
	}

	ret := make([]string, 0, len(crd.Spec.Versions))
	for _, v := range crd.Spec.Versions {
		if !v.Served {
			continue
		}
		ret = append(ret, byGroupVersionResourceKeyFunc(crd.Spec.Group, v.Name, crd.Spec.Names.Plural))
	}

	return ret, nil
}

type crdGVRSource struct {
	crdInformer cache.SharedIndexInformer
	crdIndexer  cache.Indexer
}

// Hard-code built in types that support list+watch.
var builtInInformableTypes map[schema.GroupVersionResource]GVRPartialMetadata = map[schema.GroupVersionResource]GVRPartialMetadata{
	gvrFor("", "v1", "configmaps"):                                                          withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "ConfigMap", "configmap"),
	gvrFor("", "v1", "events"):                                                              withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Event", "event"),
	gvrFor("", "v1", "namespaces"):                                                          withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "Namespace", "namespace"),
	gvrFor("", "v1", "resourcequotas"):                                                      withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "ResourceQuota", "resourcequota"),
	gvrFor("", "v1", "secrets"):                                                             withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Secret", "secret"),
	gvrFor("", "v1", "serviceaccounts"):                                                     withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "ServiceAccount", "serviceaccount"),
	gvrFor("certificates.k8s.io", "v1", "certificatesigningrequests"):                       withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "CertificateSigningRequest", "certificatesigningrequest"),
	gvrFor("coordination.k8s.io", "v1", "leases"):                                           withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Lease", "lease"),
	gvrFor("rbac.authorization.k8s.io", "v1", "clusterroles"):                               withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "ClusterRole", "clusterrole"),
	gvrFor("rbac.authorization.k8s.io", "v1", "clusterrolebindings"):                        withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "ClusterRoleBinding", "clusterrolebinding"),
	gvrFor("rbac.authorization.k8s.io", "v1", "roles"):                                      withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Role", "role"),
	gvrFor("rbac.authorization.k8s.io", "v1", "rolebindings"):                               withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "RoleBinding", "rolebinding"),
	gvrFor("events.k8s.io", "v1", "events"):                                                 withGVRPartialMetadata(apiextensionsv1.NamespaceScoped, "Event", "event"),
	gvrFor("admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations"):           withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "MutatingWebhookConfiguration", "mutatingwebhookconfiguration"),
	gvrFor("admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations"):         withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "ValidatingWebhookConfiguration", "validatingwebhookconfiguration"),
	gvrFor("admissionregistration.k8s.io", "v1alpha1", "validatingadmissionpolicies"):       withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "ValidatingAdmissionPolicy", "validatingadmissionpolicy"),
	gvrFor("admissionregistration.k8s.io", "v1alpha1", "validatingadmissionpolicybindings"): withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "ValidatingAdmissionPolicyBinding", "validatingadmissionpolicybinding"),
	gvrFor("apiextensions.k8s.io", "v1", "customresourcedefinitions"):                       withGVRPartialMetadata(apiextensionsv1.ClusterScoped, "CustomResourceDefinition", "customresourcedefinition"),
}

func (s *crdGVRSource) GVRs() map[schema.GroupVersionResource]GVRPartialMetadata {
	crdGVRs := s.crdIndexer.ListIndexFuncValues(byGroupVersionResourceIndex)
	result := make(map[schema.GroupVersionResource]GVRPartialMetadata, len(crdGVRs)+len(builtInInformableTypes))
	for gvr, metadata := range builtInInformableTypes {
		result[gvr] = metadata
	}
	for _, indexedValue := range crdGVRs {
		parts := strings.Split(indexedValue, "/")
		gvr := gvrFor(parts[0], parts[1], parts[2])

		obj, err := indexers.ByIndex[*apiextensionsv1.CustomResourceDefinition](s.crdIndexer, byGroupVersionResourceIndex, byGroupVersionResourceKeyFunc(gvr.Group, gvr.Version, gvr.Resource))
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}
		if len(obj) == 0 {
			utilruntime.HandleError(fmt.Errorf("unable to retrieve CRD for GVR: %s", gvr))
			continue
		}
		// We assume CRDs partial metadata for the same GVR are constant
		crd := obj[0]
		result[gvr] = withGVRPartialMetadata(crd.Spec.Scope, crd.Spec.Names.Kind, crd.Spec.Names.Singular)
	}
	return result
}

func (s *crdGVRSource) Ready() bool {
	return s.crdInformer.HasSynced()
}

func crdIsEstablished(obj interface{}) bool {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("unable to determine if CRD is established - got type %T instead", obj))
		return false
	}

	return apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established)
}

func (s *crdGVRSource) Subscribe() <-chan struct{} {
	changes := make(chan struct{}, 1)

	notifyChange := func() {
		select {
		case changes <- struct{}{}:
			klog.Background().V(4).Info("enqueued CRD change notification")
		default:
			klog.Background().V(5).Info("dropping CRD change notification because a notification is already pending")
		}
	}

	// When CRDs change, send a notification that we might need to add/remove informers.
	_, _ = s.crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if crdIsEstablished(obj) {
				notifyChange()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldEstablished := crdIsEstablished(oldObj)
			newEstablished := crdIsEstablished(newObj)
			if newEstablished || oldEstablished != newEstablished {
				notifyChange()
			}
		},
		DeleteFunc: func(obj interface{}) {
			notifyChange()
		},
	})

	return changes
}

type restMapper struct {
	meta.RESTMapper
}

func (r *restMapper) Reset() {
	// NOOP: this is called by the Kubernetes garbage collector controller, that assumes discovery
	// is refreshed periodically. As this shared informer factory pushes events whenever discovery
	// changes, there is no need to reset the REST mapper during the periodic re-sync of the GC monitors.
}

var _ meta.ResettableRESTMapper = &restMapper{}
