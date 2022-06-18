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
	"strings"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	resyncPeriod = 10 * time.Hour
)

type clusterDiscovery interface {
	WithCluster(name logicalcluster.Name) discovery.DiscoveryInterface
}

// DynamicDiscoverySharedInformerFactory is a SharedInformerFactory that
// dynamically discovers new types and begins informing on them.
type DynamicDiscoverySharedInformerFactory struct {
	workspaceLister tenancylisters.ClusterWorkspaceLister
	disco           clusterDiscovery
	dynamicClient   dynamic.Interface
	handlers        []GVREventHandler
	filterFunc      func(interface{}) bool
	pollInterval    time.Duration
	indexers        cache.Indexers

	mu            sync.RWMutex // guards gvrs
	gvrs          map[schema.GroupVersionResource]struct{}
	informers     map[schema.GroupVersionResource]informers.GenericInformer
	informerStops map[schema.GroupVersionResource]chan struct{}
	terminating   bool
}

// IndexerFor returns the indexer for the given type GVR.
func (d *DynamicDiscoverySharedInformerFactory) IndexerFor(gvr schema.GroupVersionResource) cache.Indexer {
	return d.InformerForResource(gvr).Informer().GetIndexer()
}

// InformerForResource returns the GenericInformer for gvr, creating it if needed.
func (d *DynamicDiscoverySharedInformerFactory) InformerForResource(gvr schema.GroupVersionResource) informers.GenericInformer {
	// See if we already have it
	d.mu.RLock()
	inf := d.informers[gvr]
	d.mu.RUnlock()

	if inf != nil {
		return inf
	}

	// Grab the write lock, then find-or-create
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.informerForResourceLockHeld(gvr)
}

// informerForResourceLockHeld returns the GenericInformer for gvr, creating it if needed. The caller must have the write
// lock before calling this method.
func (d *DynamicDiscoverySharedInformerFactory) informerForResourceLockHeld(gvr schema.GroupVersionResource) informers.GenericInformer {
	// In case it was created in between the initial check while the rlock was held and when the write lock was
	// acquired, return it instead of creating a 2nd copy and overwriting.
	inf := d.informers[gvr]
	if inf != nil {
		return inf
	}

	// Definitely need to create it
	inf = dynamicinformer.NewFilteredDynamicInformer(
		d.dynamicClient,
		gvr,
		corev1.NamespaceAll,
		resyncPeriod,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		nil,
	)

	// Store in cache
	d.informers[gvr] = inf

	return inf
}

// Listers returns a map of per-resource-type listers for all types that are
// known by this informer factory, and that are synced.
//
// If any informers aren't synced, their GVRs are returned so that they can be
// checked and processed later.
func (d *DynamicDiscoverySharedInformerFactory) Listers() (listers map[schema.GroupVersionResource]cache.GenericLister, notSynced []schema.GroupVersionResource) {
	listers = map[schema.GroupVersionResource]cache.GenericLister{}

	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.terminating {
		return
	}

	for gvr := range d.gvrs {
		// We have the read lock so d.informers is fully populated for all the gvrs in d.gvrs. We use d.informers
		// directly instead of calling either InformerForResource or informerForResourceLockHeld.
		informer := d.informers[gvr]
		if !informer.Informer().HasSynced() {
			notSynced = append(notSynced, gvr)
			continue
		}

		listers[gvr] = informer.Lister()
	}

	return listers, notSynced
}

// NewDynamicDiscoverySharedInformerFactory returns a factory for shared
// informers that discovers new types and informs on updates to resources of
// those types.
func NewDynamicDiscoverySharedInformerFactory(
	workspaceLister tenancylisters.ClusterWorkspaceLister,
	disco clusterDiscovery,
	dynClient dynamic.Interface,
	filterFunc func(obj interface{}) bool,
	pollInterval time.Duration,
) *DynamicDiscoverySharedInformerFactory {
	return &DynamicDiscoverySharedInformerFactory{
		workspaceLister: workspaceLister,
		disco:           disco,
		dynamicClient:   dynClient,
		filterFunc:      filterFunc,
		gvrs:            make(map[schema.GroupVersionResource]struct{}),
		pollInterval:    pollInterval,
		informers:       make(map[schema.GroupVersionResource]informers.GenericInformer),
		informerStops:   make(map[schema.GroupVersionResource]chan struct{}),
	}
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

func (d *DynamicDiscoverySharedInformerFactory) AddEventHandler(handler GVREventHandler) {
	d.handlers = append(d.handlers, handler)
}

func (d *DynamicDiscoverySharedInformerFactory) AddIndexers(indexers cache.Indexers) error {
	if d.indexers == nil {
		d.indexers = map[string]cache.IndexFunc{}
	}
	for name, indexer := range indexers {
		if _, found := d.indexers[name]; found {
			return fmt.Errorf("indexer %q already exists", name)
		}
		d.indexers[name] = indexer
	}

	return nil
}

func (d *DynamicDiscoverySharedInformerFactory) Start(ctx context.Context) {
	// Immediately discover types and start informing.
	// TODO: Feed any failure to discover types into /readyz, instead of
	// panicking.
	if err := d.discoverTypes(ctx); err != nil {
		// TODO(sttts): don't klog.Fatal, but return an error
		klog.Fatalf("Error discovering initial types: %v", err)
	}

	// Poll for new types in the background.
	ticker := time.NewTicker(d.pollInterval)
	go func() {
		defer func() {
			d.mu.Lock()
			defer d.mu.Unlock()

			// tear down all informers when done.
			for _, stopCh := range d.informerStops {
				close(stopCh)
			}

			// Note: it does not matter if after this another informer is added. It won't be started without
			// calling discoverTypes.
		}()

		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				if err := d.discoverTypes(ctx); err != nil {
					klog.Errorf("Error discovering types: %v", err)
				}
			}
		}
	}()
}

func (d *DynamicDiscoverySharedInformerFactory) discoverTypes(ctx context.Context) error {
	latest := map[schema.GroupVersionResource]struct{}{}

	// Get a list of all the logical cluster names. We'll get discovery from all of them, union all the GVRs, and use
	// that union for the informer.

	// TODO(ncdc): this may not scale well. Watchable discovery or something like that
	// is a better long term solution.
	workspaces, err := d.workspaceLister.List(labels.Everything())
	if err != nil {
		return err
	}
	for i := range workspaces {
		logicalClusterName := logicalcluster.From(workspaces[i]).Join(workspaces[i].Name).String()

		klog.Infof("Discovering types for logical cluster %q", logicalClusterName)
		rs, err := d.disco.WithCluster(logicalcluster.New(logicalClusterName)).ServerPreferredResources()
		if err != nil {
			return err
		}
		for _, r := range rs {
			gv, err := schema.ParseGroupVersion(r.GroupVersion)
			if err != nil {
				return err
			}
			for _, ai := range r.APIResources {
				gvr := gv.WithResource(ai.Name)

				if strings.Contains(ai.Name, "/") {
					// foo/status, pods/exec, namespace/finalize, etc.
					continue
				}
				if !ai.Namespaced {
					// Ignore cluster-scoped things.
					continue
				}
				if !sets.NewString([]string(ai.Verbs)...).HasAll("list", "watch") {
					klog.V(4).InfoS("resource is not list+watchable", "logical-cluster", logicalClusterName, "group", gv.Group, "version", gv.Version, "resource", ai.Name, "verbs", ai.Verbs)
					continue
				}

				latest[gvr] = struct{}{}
			}
		}
	}

	// Grab a read lock to compare against d.gvrs to see if we need to start or stop any informers
	d.mu.RLock()
	informersToAdd, informersToRemove := d.calculateInformersLockHeld(latest)
	d.mu.RUnlock()

	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		return nil
	}

	// We have to add/remove, so we need the write lock
	d.mu.Lock()
	defer d.mu.Unlock()

	// Recalculate in case another goroutine did this work in between when we had the read lock and when we acquired
	// the write lock
	informersToAdd, informersToRemove = d.calculateInformersLockHeld(latest)
	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		return nil
	}

	// Now we definitely need to do this work
	for i := range informersToAdd {
		gvr := informersToAdd[i]

		klog.Infof("Adding dynamic informer for %q", gvr)

		// We have the write lock, so call the LH variant
		inf := d.informerForResourceLockHeld(gvr).Informer()
		inf.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: d.filterFunc,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					for _, h := range d.handlers {
						h.OnAdd(gvr, obj)
					}
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					for _, h := range d.handlers {
						h.OnUpdate(gvr, oldObj, newObj)
					}
				},
				DeleteFunc: func(obj interface{}) {
					for _, h := range d.handlers {
						h.OnDelete(gvr, obj)
					}
				},
			},
		})

		if err := inf.GetIndexer().AddIndexers(d.indexers); err != nil {
			return err
		}

		// Set up a stop channel for this specific informer
		stop := make(chan struct{})
		go inf.Run(stop)

		// And store it
		d.informerStops[gvr] = stop
	}

	for i := range informersToRemove {
		gvr := informersToRemove[i]

		klog.Infof("Removing dynamic informer for %q", gvr)

		stop, ok := d.informerStops[gvr]
		if ok {
			klog.V(4).Infof("Closing stop channel for dynamic informer for %q", gvr)
			close(stop)
		}

		klog.V(4).Infof("Removing dynamic informer from maps for %q", gvr)
		delete(d.informers, gvr)
		delete(d.informerStops, gvr)
	}

	d.gvrs = latest

	return nil
}

func (d *DynamicDiscoverySharedInformerFactory) calculateInformersLockHeld(latest map[schema.GroupVersionResource]struct{}) (toAdd, toRemove []schema.GroupVersionResource) {
	for gvr := range latest {
		if _, found := d.gvrs[gvr]; !found {
			toAdd = append(toAdd, gvr)
		}
	}

	for gvr := range d.gvrs {
		if _, found := latest[gvr]; !found {
			toRemove = append(toRemove, gvr)
		}
	}

	return toAdd, toRemove
}
