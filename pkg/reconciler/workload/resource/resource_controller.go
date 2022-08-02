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

package resource

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const controllerName = "kcp-workload-resource-scheduler"

// NewController returns a new Controller which schedules resources in scheduled namespaces.
func NewController(
	dynamicClusterClient dynamic.Interface,
	ddsif *informer.DynamicDiscoverySharedInformerFactory,
	syncTargetInformer workloadinformers.SyncTargetInformer,
	namespaceInformer coreinformers.NamespaceInformer,
) (*Controller, error) {
	resourceQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-namespace-resource")
	gvrQueue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-namespace-gvr")

	c := &Controller{
		resourceQueue: resourceQueue,
		gvrQueue:      gvrQueue,

		dynClusterClient: dynamicClusterClient,

		namespaceLister: namespaceInformer.Lister(),

		syncTargetLister:  syncTargetInformer.Lister(),
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),

		ddsif: ddsif,
	}

	namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: filterNamespace,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.enqueueNamespace(obj) },
			UpdateFunc: func(old, obj interface{}) {
				oldNS := old.(*corev1.Namespace)
				newNS := obj.(*corev1.Namespace)
				if !reflect.DeepEqual(scheduleStateLabels(oldNS.Labels), scheduleStateLabels(newNS.Labels)) ||
					!reflect.DeepEqual(scheduleStateAnnotations(oldNS.Annotations), scheduleStateAnnotations(newNS.Annotations)) {
					c.enqueueNamespace(obj)
				}

			},
			DeleteFunc: nil, // Nothing to do.
		},
	})

	c.ddsif.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc:    func(gvr schema.GroupVersionResource, obj interface{}) { c.enqueueResource(gvr, obj) },
		UpdateFunc: func(gvr schema.GroupVersionResource, _, obj interface{}) { c.enqueueResource(gvr, obj) },
		DeleteFunc: nil, // Nothing to do.
	})

	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.enqueueSyncTarget(obj)
		},
	})

	return c, nil
}

func scheduleStateLabels(ls map[string]string) map[string]string {
	ret := make(map[string]string, len(ls))
	for k, v := range ls {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			ret[k] = v
		}
	}
	return ret
}

func scheduleStateAnnotations(ls map[string]string) map[string]string {
	ret := make(map[string]string, len(ls))
	for k, v := range ls {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix) {
			ret[k] = v
		}
	}
	return ret
}

type Controller struct {
	resourceQueue workqueue.RateLimitingInterface
	gvrQueue      workqueue.RateLimitingInterface

	dynClusterClient dynamic.Interface

	namespaceLister corelisters.NamespaceLister

	syncTargetLister  workloadlisters.SyncTargetLister
	syncTargetIndexer cache.Indexer

	ddsif *informer.DynamicDiscoverySharedInformerFactory
}

func filterNamespace(obj interface{}) bool {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	_, name := clusters.SplitClusterAwareKey(clusterAwareName)
	if namespaceBlocklist.Has(name) {
		klog.V(2).Infof("Skipping syncing namespace %q", name)
		return false
	}
	return true
}

func (c *Controller) enqueueResource(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrstr := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")
	c.resourceQueue.Add(gvrstr + "::" + key)
}

func (c *Controller) enqueueGVR(gvr schema.GroupVersionResource) {
	gvrstr := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")
	c.gvrQueue.Add(gvrstr)
}

func (c *Controller) enqueueNamespace(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	ns, err := c.namespaceLister.Get(key)
	if err != nil && !errors.IsNotFound(err) {
		runtime.HandleError(err)
		return
	}
	if err := c.enqueueResourcesForNamespace(ns); err != nil {
		runtime.HandleError(err)
		return
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.resourceQueue.ShutDown()
	defer c.gvrQueue.ShutDown()

	klog.Info("Starting Resource scheduler")
	defer klog.Info("Shutting down Resource scheduler")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startResourceWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startGVRWorker(ctx) }, time.Second, ctx.Done())
	}
	<-ctx.Done()
}

func (c *Controller) startResourceWorker(ctx context.Context) {
	for processNext(ctx, c.resourceQueue, c.processResource) {
	}
}
func (c *Controller) startGVRWorker(ctx context.Context) {
	for processNext(ctx, c.gvrQueue, c.processGVR) {
	}
}

func processNext(
	ctx context.Context,
	queue workqueue.RateLimitingInterface,
	processFunc func(ctx context.Context, key string) error,
) bool {
	// Wait until there is a new item in the working  queue
	k, quit := queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer queue.Done(key)

	if err := processFunc(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		queue.AddRateLimited(key)
		return true
	}
	queue.Forget(key)
	return true
}

// key is gvr::KEY
func (c *Controller) processResource(ctx context.Context, key string) error {
	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		klog.Errorf("Error parsing key %q; dropping", key)
		return nil
	}
	gvrstr := parts[0]
	gvr, _ := schema.ParseResourceArg(gvrstr)
	if gvr == nil {
		klog.Errorf("Error parsing GVR %q; dropping", gvrstr)
		return nil
	}
	key = parts[1]

	inf, err := c.ddsif.ForResource(*gvr)
	if err != nil {
		return err
	}
	obj, exists, err := inf.Informer().GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Error getting %q GVR %q from indexer: %v", key, gvrstr, err)
		return err
	}
	if !exists {
		klog.V(3).Infof("object %q GVR %q does not exist", key, gvrstr)
		return nil
	}
	unstr, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("object was not Unstructured, dropping: %T", obj)
		return nil
	}
	unstr = unstr.DeepCopy()

	// Get logical cluster name.
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split key %q, dropping: %v", key, err)
		return nil
	}
	lclusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)
	return c.reconcileResource(ctx, lclusterName, unstr, gvr)
}

func (c *Controller) processGVR(ctx context.Context, gvrstr string) error {
	gvr, _ := schema.ParseResourceArg(gvrstr)
	if gvr == nil {
		klog.Errorf("Error parsing GVR %q; dropping", gvrstr)
		return nil
	}
	return c.reconcileGVR(*gvr)
}

// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
var namespaceBlocklist = sets.NewString("kube-system", "kube-public", "kube-node-lease")

// enqueueResourcesForNamespace adds the resources contained by the given
// namespace to the queue if there scheduling label differs from the namespace's.
func (c *Controller) enqueueResourcesForNamespace(ns *corev1.Namespace) error {
	clusterName := logicalcluster.From(ns)

	nsLocations, nsDeleting := locations(ns.Annotations, ns.Labels, true)

	klog.V(4).Infof("enqueueResourcesForNamespace(%s|%s): getting listers", clusterName, ns.Name)
	listers, notSynced := c.ddsif.Listers()
	for gvr, lister := range listers {
		objs, err := lister.ByNamespace(ns.Name).List(labels.Everything())
		if err != nil {
			return err
		}

		klog.V(4).Infof("enqueueResourcesForNamespace(%s|%s): got %d items for GVR %q", clusterName, ns.Name, len(objs), gvr.String())

		var enqueuedResources []string
		for _, obj := range objs {
			u := obj.(*unstructured.Unstructured)

			// TODO(ncdc): remove this when we have namespaced listers that only return for the scoped cluster (https://github.com/kcp-dev/kcp/issues/685).
			if logicalcluster.From(u) != clusterName {
				continue
			}

			objLocations, objDeleting := locations(u.GetAnnotations(), u.GetLabels(), false)
			if !objLocations.Equal(nsLocations) || !objDeleting.Equal(nsDeleting) {
				c.enqueueResource(gvr, obj)

				if klog.V(2).Enabled() && !klog.V(4).Enabled() && len(enqueuedResources) < 10 {
					enqueuedResources = append(enqueuedResources, u.GetName())
				}

				klog.V(3).Infof("Enqueuing %s %s|%s/%s to schedule to %v", gvr.GroupVersion().WithKind(u.GetKind()), logicalcluster.From(ns), ns.Name, u.GetName(), nsLocations.List())
			} else {
				klog.V(4).Infof("Skipping %s %s|%s/%s because it is already scheduled to %v", gvr.GroupVersion().WithKind(u.GetKind()), logicalcluster.From(ns), ns.Name, u.GetName(), nsLocations.List())
			}
		}

		if len(enqueuedResources) > 0 {
			if len(enqueuedResources) == 10 {
				enqueuedResources = append(enqueuedResources, "...")
			}
			klog.V(2).Infof("Enqueuing some GVR %q in namespace %s|%s to schedule to %v: %s", gvr, logicalcluster.From(ns), ns.Name, nsLocations.List(), strings.Join(enqueuedResources, ","))
		}
	}

	// For all types whose informer hasn't synced yet, enqueue a workqueue
	// item to check that GVR again later (reconcileGVR, above).
	for _, gvr := range notSynced {
		klog.V(3).Infof("Informer for GVR %q is not synced, needed for namespace %s|%s; re-enqueueing", gvr, logicalcluster.From(ns), ns.Name)
		c.enqueueGVR(gvr)
	}

	return nil
}

func (c *Controller) enqueueSyncTarget(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, name := clusters.SplitClusterAwareKey(key)
	finalizer := syncershared.SyncerFinalizerNamePrefix + workloadv1alpha1.ToSyncTargetKey(clusterName, name)

	listers, _ := c.ddsif.Listers()
	queued := map[string]int{}
	for gvr := range listers {
		inf, err := c.ddsif.ForResource(gvr)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		objs, err := inf.Informer().GetIndexer().ByIndex(indexers.BySyncerFinalizerKey, finalizer)
		if err != nil {
			runtime.HandleError(err)
			continue // ignore
		}
		if len(objs) == 0 {
			continue
		}
		for _, obj := range objs {
			c.enqueueResource(gvr, obj)
		}
		queued[gvr.String()] = len(objs)
	}
	if len(queued) > 0 {
		klog.V(2).InfoS("Queued GVRs with finalizer %s because SyncTarget %s|%s with key %s got deleted: %v", clusterName, name, finalizer, queued)
	}
}

func locations(annotations, labels map[string]string, skipPending bool) (locations sets.String, deleting sets.String) {
	locations = sets.NewString()
	deleting = sets.NewString()

	for k, v := range labels {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) && (!skipPending || v == string(workloadv1alpha1.ResourceStateSync)) {
			locations.Insert(strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix))
		}
	}
	for k := range annotations {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix) {
			deleting.Insert(strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix))
		}
	}
	return
}
