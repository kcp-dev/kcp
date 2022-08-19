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

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
)

const (
	resyncPeriod = 10 * time.Hour

	byGroupFirstFoundVersionResourceIndex = "byGroup-firstFoundVersion-resource"
)

// DynamicDiscoverySharedInformerFactory is a SharedInformerFactory that
// dynamically discovers new types and begins informing on them.
type DynamicDiscoverySharedInformerFactory struct {
	dynamicClient     dynamic.Interface
	filterFunc        func(interface{}) bool
	indexers          cache.Indexers
	crdIndexer        cache.Indexer
	crdInformerSynced cache.InformerSynced

	// handlersLock protects multiple writers racing to update handlers.
	handlersLock sync.Mutex
	handlers     atomic.Value

	// updateCh receives notifications for all CRD add/update/delete events, so we can start new informers and stop
	// informers no longer needed.
	updateCh chan struct{}

	informersLock    sync.RWMutex
	informers        map[schema.GroupVersionResource]informers.GenericInformer
	startedInformers map[schema.GroupVersionResource]bool
	informerStops    map[schema.GroupVersionResource]chan struct{}
	discoveryData    []*metav1.APIResourceList

	// Support subscribers (e.g. quota) that want to know when informers/discovery have changed.
	subscribersLock sync.Mutex
	subscribers     map[string]chan<- struct{}
}

// NewDynamicDiscoverySharedInformerFactory returns a factory for shared
// informers that discovers new types and informs on updates to resources of
// those types.
func NewDynamicDiscoverySharedInformerFactory(
	cfg *rest.Config,
	filterFunc func(obj interface{}) bool,
	crdInformer apiextensionsinformers.CustomResourceDefinitionInformer,
	indexers cache.Indexers,
) (*DynamicDiscoverySharedInformerFactory, error) {
	cfg = rest.AddUserAgent(rest.CopyConfig(cfg), "kcp-partial-metadata-informers")

	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(cfg)
	if err != nil {
		return nil, err
	}

	f := &DynamicDiscoverySharedInformerFactory{
		dynamicClient:     metadataClusterClient.Cluster(logicalcluster.Wildcard),
		filterFunc:        filterFunc,
		indexers:          indexers,
		crdIndexer:        crdInformer.Informer().GetIndexer(),
		crdInformerSynced: crdInformer.Informer().HasSynced,

		// Use a buffered channel of size 1 to allow enqueuing 1 update notification
		updateCh: make(chan struct{}, 1),

		informers:        make(map[schema.GroupVersionResource]informers.GenericInformer),
		startedInformers: make(map[schema.GroupVersionResource]bool),
		informerStops:    make(map[schema.GroupVersionResource]chan struct{}),

		subscribers: make(map[string]chan<- struct{}),
	}

	f.handlers.Store([]GVREventHandler{})

	// Add an index function that indexes a CRD by its group/firstServedVersion/resource. We only need the first
	// served version because this shared informer factory is expected to be using a wildcard client for partial
	// metadata only. In this instance, version does not matter, because a wildcard partial metadata list request
	// for CRs always serves all CRs for the group-resource, regardless of storage version.
	if err := crdInformer.Informer().AddIndexers(cache.Indexers{
		byGroupFirstFoundVersionResourceIndex: func(obj interface{}) ([]string, error) {
			crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
			if !ok {
				return nil, fmt.Errorf("%T is not a CustomResourceDefinition", obj)
			}

			firstServedVersion := ""
			for _, version := range crd.Spec.Versions {
				if !version.Served {
					continue
				}
				firstServedVersion = version.Name
				break
			}

			if firstServedVersion == "" {
				return []string{}, nil
			}

			group := crd.Spec.Group
			resource := crd.Spec.Names.Plural

			indexValue := fmt.Sprintf("%s/%s/%s", group, firstServedVersion, resource)
			return []string{indexValue}, nil
		},
	}); err != nil {
		return nil, err
	}

	// Any time a CRD event comes in, let StartWorker() know about it
	notifyUpdateNeeded := func() {
		select {
		case f.updateCh <- struct{}{}:
			klog.V(4).InfoS("Enqueued update notification for dynamic informer recalculation")
		default:
			klog.V(5).InfoS("Dropping update notification for dynamic informer recalculation because a notification is already pending")
		}
	}

	crdIsEstablished := func(obj interface{}) bool {
		crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("unable to determine if CRD is established - got type %T instead", obj))
			return false
		}

		return apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established)
	}

	// When CRDs change, send a notification that we might need to add/remove informers.
	crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if crdIsEstablished(obj) {
				notifyUpdateNeeded()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldEstablished := crdIsEstablished(oldObj)
			newEstablished := crdIsEstablished(newObj)
			if newEstablished || oldEstablished != newEstablished {
				notifyUpdateNeeded()
			}
		},
		DeleteFunc: func(obj interface{}) {
			notifyUpdateNeeded()
		},
	})

	return f, nil
}

// ForResource returns the GenericInformer for gvr, creating it if needed. The GenericInformer must be started
// by calling Start on the DynamicDiscoverySharedInformerFactory before the GenericInformer can be used.
func (d *DynamicDiscoverySharedInformerFactory) ForResource(gvr schema.GroupVersionResource) (informers.GenericInformer, error) {
	// See if we already have it
	d.informersLock.RLock()
	inf := d.informers[gvr]
	d.informersLock.RUnlock()

	if inf != nil {
		return inf, nil
	}

	// Grab the write lock, then find-or-create
	d.informersLock.Lock()
	defer d.informersLock.Unlock()

	return d.informerForResourceLockHeld(gvr), nil
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

	klog.V(2).Infof("Adding dynamic informer for %q", gvr)

	// TODO(ncdc) remove NamespaceIndex when scoping is fully integrated
	indexers := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}

	for k, v := range d.indexers {
		if k == cache.NamespaceIndex {
			// Don't allow overriding NamespaceIndex
			continue
		}

		indexers[k] = v
	}

	// Definitely need to create it
	inf = dynamicinformer.NewFilteredDynamicInformer(
		d.dynamicClient,
		gvr,
		corev1.NamespaceAll,
		resyncPeriod,
		indexers,
		nil,
	)

	inf.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
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

// Listers returns a map of per-resource-type listers for all types that are
// known by this informer factory, and that are synced.
//
// If any informers aren't synced, their GVRs are returned so that they can be
// checked and processed later.
func (d *DynamicDiscoverySharedInformerFactory) Listers() (listers map[schema.GroupVersionResource]cache.GenericLister, notSynced []schema.GroupVersionResource) {
	listers = map[schema.GroupVersionResource]cache.GenericLister{}

	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	for gvr, informer := range d.informers {
		// We have the read lock so d.informers is fully populated for all the gvrs in d.gvrs. We use d.informers
		// directly instead of calling either ForResource or informerForResourceLockHeld.
		if !informer.Informer().HasSynced() {
			notSynced = append(notSynced, gvr)
			continue
		}

		listers[gvr] = informer.Lister()
	}

	return listers, notSynced
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
func (d *DynamicDiscoverySharedInformerFactory) StartWorker(ctx context.Context) {
	defer func() {
		d.informersLock.Lock()

		for _, stopCh := range d.informerStops {
			close(stopCh)
		}

		d.informersLock.Unlock()
	}()

	if !cache.WaitForNamedCacheSync("kcp-ddsif-crd", ctx.Done(), d.crdInformerSynced) {
		klog.Errorf("CRD informer never synced")
		return
	}

	// Now that the CRD informer has synced, do an initial update
	d.updateInformers()

	// Use UntilWithContext here so that we only check updateCh at most once every second. Because a flurry of several
	// watch events for CRDs can come in quickly, this effectively "batches" them, so we aren't recalculating the
	// informers for each watch event in a tightly grouped set of events.
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		klog.V(5).InfoS("Waiting for notification")
		select {
		case <-ctx.Done():
			return
		case <-d.updateCh:
		}

		klog.V(5).InfoS("Notification received")
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

func builtInInformableTypes() map[schema.GroupVersionResource]struct{} {
	// Hard-code built in types that support list+watch
	latest := map[schema.GroupVersionResource]struct{}{
		gvrFor("", "v1", "configmaps"):                                                   {},
		gvrFor("", "v1", "events"):                                                       {},
		gvrFor("", "v1", "limitranges"):                                                  {},
		gvrFor("", "v1", "namespaces"):                                                   {},
		gvrFor("", "v1", "resourcequotas"):                                               {},
		gvrFor("", "v1", "secrets"):                                                      {},
		gvrFor("", "v1", "serviceaccounts"):                                              {},
		gvrFor("certificates.k8s.io", "v1", "certificatesigningrequests"):                {},
		gvrFor("coordination.k8s.io", "v1", "leases"):                                    {},
		gvrFor("rbac.authorization.k8s.io", "v1", "clusterroles"):                        {},
		gvrFor("rbac.authorization.k8s.io", "v1", "clusterrolebindings"):                 {},
		gvrFor("rbac.authorization.k8s.io", "v1", "roles"):                               {},
		gvrFor("rbac.authorization.k8s.io", "v1", "rolebindings"):                        {},
		gvrFor("flowcontrol.apiserver.k8s.io", "v1beta2", "flowschemas"):                 {},
		gvrFor("flowcontrol.apiserver.k8s.io", "v1beta2", "prioritylevelconfigurations"): {},
		gvrFor("events.k8s.io", "v1", "events"):                                          {},
		gvrFor("admissionregistration.k8s.io", "v1", "mutatingwebhookconfigurations"):    {},
		gvrFor("admissionregistration.k8s.io", "v1", "validatingwebhookconfigurations"):  {},
		gvrFor("apiextensions.k8s.io", "v1", "customresourcedefinitions"):                {},
	}

	return latest
}

func (d *DynamicDiscoverySharedInformerFactory) updateInformers() {
	klog.V(5).InfoS("Determining dynamic informer additions and removals")

	latest := builtInInformableTypes()

	// Get the unique set of Group(Version)Resources (version doesn't matter because we're expecting a wildcard
	// partial metadata client, but we need a version in the request, so we need it here) and add them to latest.
	crdGVRs := d.crdIndexer.ListIndexFuncValues(byGroupFirstFoundVersionResourceIndex)
	for _, s := range crdGVRs {
		parts := strings.Split(s, "/")
		group := parts[0]
		version := parts[1]
		resource := parts[2]

		// Don't start a dynamic informer for tenancy.kcp.dev/v1beta1 Workspaces. These are a virtual projection of
		// data from tenancy.kcp.dev/v1alpha1 ClusterWorkspaces. Starting an informer for them causes problems when
		// the virtual-workspaces server is deployed separately. See https://github.com/kcp-dev/kcp/issues/1654 for
		// more details.
		if group == tenancy.GroupName && version == "v1beta1" && resource == "workspaces" {
			continue
		}

		latest[gvrFor(group, version, resource)] = struct{}{}
	}

	// Grab a read lock to compare against d.informers to see if we need to start or stop any informers
	d.informersLock.RLock()
	informersToAdd, informersToRemove := d.calculateInformersLockHeld(latest)
	d.informersLock.RUnlock()

	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		klog.V(5).InfoS("No changes")
		return
	}

	// We have to add/remove, so we need the write lock
	d.informersLock.Lock()
	defer d.informersLock.Unlock()

	// Recalculate in case another goroutine did this work in between when we had the read lock and when we acquired
	// the write lock
	informersToAdd, informersToRemove = d.calculateInformersLockHeld(latest)
	if len(informersToAdd) == 0 && len(informersToRemove) == 0 {
		klog.V(5).InfoS("No changes")
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

		klog.V(2).Infof("Removing dynamic informer for %q", gvr)

		stop, ok := d.informerStops[gvr]
		if ok {
			klog.V(4).Infof("Closing stop channel for dynamic informer for %q", gvr)
			close(stop)
		}

		klog.V(4).Infof("Removing dynamic informer from maps for %q", gvr)
		delete(d.informers, gvr)
		delete(d.informerStops, gvr)
		delete(d.startedInformers, gvr)
	}

	d.discoveryData = gvrsToDiscoveryData(latest)

	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	for id, ch := range d.subscribers {
		klog.V(4).InfoS("Attempting to notify discovery subscriber", "id", id)
		select {
		case ch <- struct{}{}:
			klog.V(4).InfoS("Successfully notified discovery subscriber", "id", id)
		default:
			klog.V(4).InfoS("Unable to notify discovery subscriber - channel full", "id", id)
		}
	}

}

// gvrsToDiscoveryData returns "fake"/simulated discovery data for all the resources covered by the factory. It only
// includes enough data in each APIResource to support what kcp currently needs (scheduling, placement, quota).
func gvrsToDiscoveryData(gvrs map[schema.GroupVersionResource]struct{}) []*metav1.APIResourceList {
	var discoveryData []*metav1.APIResourceList
	gvResources := make(map[schema.GroupVersion][]metav1.APIResource)

	for gvr := range gvrs {
		gv := gvr.GroupVersion()

		gvResources[gv] = append(gvResources[gv], metav1.APIResource{
			Name:    gvr.Resource,
			Group:   gvr.Group,
			Version: gvr.Version,
			// Everything we're informing on supports these
			Verbs: []string{"create", "list", "watch", "delete"},
		})
	}

	for gv, resources := range gvResources {
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})

		discoveryData = append(discoveryData, &metav1.APIResourceList{
			GroupVersion: gv.String(),
			APIResources: resources,
		})
	}

	sort.Slice(discoveryData, func(i, j int) bool {
		return discoveryData[i].GroupVersion < discoveryData[j].GroupVersion
	})

	return discoveryData
}

// DiscoveryData implements resourcequota.NamespacedResourcesFunc and is intended to be used by the quota subsystem.
func (d *DynamicDiscoverySharedInformerFactory) DiscoveryData() ([]*metav1.APIResourceList, error) {
	d.informersLock.RLock()
	defer d.informersLock.RUnlock()

	ret := make([]*metav1.APIResourceList, len(d.discoveryData))
	for i, apiResourceList := range d.discoveryData {
		ret[i] = apiResourceList.DeepCopy()
	}

	return ret, nil
}

// Start starts any informers that have been created but not yet started. The passed in stop channel is ignored;
// instead, a new stop channel is created, so the factory can properly stop the informer if/when the API is removed.
// Like other shared informer factories, this call is non-blocking.
func (d *DynamicDiscoverySharedInformerFactory) Start(_ <-chan struct{}) {
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

func (d *DynamicDiscoverySharedInformerFactory) calculateInformersLockHeld(latest map[schema.GroupVersionResource]struct{}) (toAdd, toRemove []schema.GroupVersionResource) {
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
func (d *DynamicDiscoverySharedInformerFactory) Subscribe(id string) <-chan struct{} {
	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	// Use a buffered channel so we can always send at least 1, regardless of consumer status.
	ch := make(chan struct{}, 1)
	d.subscribers[id] = ch

	return ch
}

// Unsubscribe removes the channel associated with id from future informer/discovery change notifications.
func (d *DynamicDiscoverySharedInformerFactory) Unsubscribe(id string) {
	d.subscribersLock.Lock()
	defer d.subscribersLock.Unlock()

	ch, ok := d.subscribers[id]
	if ok {
		close(ch)
	}

	delete(d.subscribers, id)
}
