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

	"go.uber.org/multierr"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	resyncPeriod = 10 * time.Hour
)

type clusterDiscovery interface {
	WithCluster(name string) discovery.DiscoveryInterface
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

	mu   sync.Mutex // guards gvrs
	gvrs sets.String
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

	for _, gvrstr := range d.gvrs.UnsortedList() {
		gvr, _ := schema.ParseResourceArg(gvrstr)
		if gvr == nil {
			klog.Errorf("parsing GVR string %q", gvrstr)
			continue
		}

		if !d.dsif.ForResource(*gvr).Informer().HasSynced() {
			notSynced = append(notSynced, *gvr)
			continue
		}

		listers[*gvr] = d.dsif.ForResource(*gvr).Lister()
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
		gvrs:            sets.NewString(),
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
	d.mu.Lock()
	defer d.mu.Unlock()

	latest := sets.NewString()
	logicalClusterNames := sets.NewString()

	// Get a list of all the logical cluster names. We'll get discovery from all of them, union all the GVRs, and use
	// that union for the informer.

	// TODO(ncdc): this may not scale well. Watchable discovery or something like that
	// is a better long term solution.
	workspaces, err := d.workspaceLister.List(labels.Everything())
	if err != nil {
		return err
	}
	for i := range workspaces {
		logicalClusterName, err := helper.EncodeLogicalClusterName(workspaces[i])
		if err != nil {
			return err
		}
		logicalClusterNames.Insert(logicalClusterName)
	}

	for _, logicalClusterName := range logicalClusterNames.List() {
		klog.Infof("Discovering types for logical cluster %q", logicalClusterName)
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

				latest.Insert(strings.Join([]string{ai.Name, gv.Version, gv.Group}, "."))
			}
		}
	}

	// Set up informers for any new types that have been discovered.
	// TODO: Stop informers for types we no longer have.
	newGVRs := latest.Difference(d.gvrs)
	var merr error
	for _, gvrstr := range newGVRs.UnsortedList() {
		klog.Infof("Adding informer for %q in %s", gvrstr, logicalClusterNames)
		gvr, _ := schema.ParseResourceArg(gvrstr)
		if gvr == nil {
			// TODO(ncdc): consider tracking where each gvrstr came from (which workspace) so we can include that in the error.
			multierr.AppendInto(&merr, fmt.Errorf("unable to parse %q to a GroupVersionResource", gvrstr))
			continue
		}

		inf := d.dsif.ForResource(*gvr).Informer()
		inf.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: d.filterFunc,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { d.handler.OnAdd(*gvr, obj) },
				UpdateFunc: func(oldObj, newObj interface{}) { d.handler.OnUpdate(*gvr, oldObj, newObj) },
				DeleteFunc: func(obj interface{}) { d.handler.OnDelete(*gvr, obj) },
			},
		})
		go inf.Run(ctx.Done())
	}
	if merr != nil {
		return merr
	}

	d.gvrs = latest
	return nil
}
