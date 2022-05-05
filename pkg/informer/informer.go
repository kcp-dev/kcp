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
	"strings"
	"sync"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	resyncPeriod = 10 * time.Hour
)

type clusterDiscovery interface {
	WithCluster(name logicalcluster.LogicalCluster) discovery.DiscoveryInterface
}

// DynamicDiscoverySharedInformerFactory is a SharedInformerFactory that
// dynamically discovers new types and begins informing on them.
type DynamicDiscoverySharedInformerFactory struct {
	workspaceLister tenancylisters.ClusterWorkspaceLister
	disco           clusterDiscovery
	dsif            dynamicinformer.DynamicSharedInformerFactory
	handler         GVREventHandler
	filterFunc      func(interface{}) bool
	pollInterval    time.Duration

	mu   sync.RWMutex // guards gvrs
	gvrs map[schema.GroupVersionResource]struct{}
}

// IndexerFor returns the indexer for the given type GVR.
func (d *DynamicDiscoverySharedInformerFactory) IndexerFor(gvr schema.GroupVersionResource) cache.Indexer {
	return d.dsif.ForResource(gvr).Informer().GetIndexer()
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

	for gvr := range d.gvrs {
		if !d.dsif.ForResource(gvr).Informer().HasSynced() {
			notSynced = append(notSynced, gvr)
			continue
		}

		listers[gvr] = d.dsif.ForResource(gvr).Lister()
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
	handler GVREventHandler,
	pollInterval time.Duration,
) DynamicDiscoverySharedInformerFactory {
	dsif := dynamicinformer.NewDynamicSharedInformerFactory(dynClient, resyncPeriod)
	return DynamicDiscoverySharedInformerFactory{
		workspaceLister: workspaceLister,
		disco:           disco,
		dsif:            dsif,
		handler:         handler,
		filterFunc:      filterFunc,
		gvrs:            map[schema.GroupVersionResource]struct{}{},
		pollInterval:    pollInterval,
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
	logicalClusterNames := map[schema.GroupVersionResource]logicalcluster.LogicalCluster{}

	// Get a list of all the logical cluster names. We'll get discovery from all of them, union all the GVRs, and use
	// that union for the informer.

	// TODO(ncdc): this may not scale well. Watchable discovery or something like that
	// is a better long term solution.
	workspaces, err := d.workspaceLister.List(labels.Everything())
	if err != nil {
		return err
	}
	for i := range workspaces {
		logicalClusterName := logicalcluster.From(workspaces[i]).Join(workspaces[i].Name)

		klog.V(4).Infof("Discovering types for logical cluster %q", logicalClusterName)
		rs, err := d.disco.WithCluster(logicalClusterName).ServerPreferredResources()
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
					continue
				}

				logicalClusterNames[gvr] = logicalClusterName
				latest[gvr] = struct{}{}
			}
		}
	}

	d.mu.RLock()
	var newGVRs []schema.GroupVersionResource
	for gvr := range latest {
		if _, found := d.gvrs[gvr]; !found {
			newGVRs = append(newGVRs, gvr)
		}
	}
	d.mu.RUnlock()

	if len(newGVRs) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Set up informers for any new types that have been discovered.
	// TODO: Stop informers for types we no longer have.
	for _, gvr := range newGVRs {
		klog.Infof("Adding informer for %q because of workspace %s (there might be others too)", gvr, logicalClusterNames[gvr])
		gvr := gvr // make copy, because we'll use it in the closure
		inf := d.dsif.ForResource(gvr).Informer()
		inf.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: d.filterFunc,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					o, _ := obj.(metav1.Object)
					klog.Infof("Adding %s %s|%s/%s", obj.(runtime.Object).GetObjectKind().GroupVersionKind().String(), logicalcluster.From(o), o.GetNamespace(), o.GetName())
					if o.GetName() == "woody" {
						klog.Infof("It's woody!")
					}
					d.handler.OnAdd(gvr, obj)
				},
				UpdateFunc: func(oldObj, newObj interface{}) { d.handler.OnUpdate(gvr, oldObj, newObj) },
				DeleteFunc: func(obj interface{}) { d.handler.OnDelete(gvr, obj) },
			},
		})
		go inf.Run(ctx.Done())

		d.gvrs[gvr] = struct{}{}
	}

	return nil
}
