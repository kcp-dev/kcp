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

package namespace

import (
	"context"
	"fmt"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	coreinformers "k8s.io/client-go/informers/core/v1"
	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	clusterinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/cluster/v1alpha1"
	clusterlisters "github.com/kcp-dev/kcp/pkg/client/listers/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/gvk"
	"github.com/kcp-dev/kcp/pkg/informer"
)

const controllerName = "namespace-scheduler"

// NewController returns a new Controller which schedules namespaced resources to a Cluster.
func NewController(
	dynClient dynamic.Interface,
	disco *discovery.DiscoveryClient,
	clusterInformer clusterinformer.ClusterInformer,
	clusterLister clusterlisters.ClusterLister,
	namespaceInformer coreinformers.NamespaceInformer,
	namespaceLister corelisters.NamespaceLister,
	namespaceClient coreclient.NamespaceInterface,
	gvkTrans *gvk.GVKTranslator,
	pollInterval time.Duration,
) *Controller {

	resourceQueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	gvrQueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	namespaceQueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	clusterQueue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	c := &Controller{
		resourceQueue:  resourceQueue,
		gvrQueue:       gvrQueue,
		namespaceQueue: namespaceQueue,
		clusterQueue:   clusterQueue,

		dynClient:       dynClient,
		clusterLister:   clusterLister,
		namespaceLister: namespaceLister,
		namespaceClient: namespaceClient,
		gvkTrans:        gvkTrans,

		syncChecks: []cache.InformerSynced{
			namespaceInformer.Informer().HasSynced,
			clusterInformer.Informer().HasSynced,
		},
	}
	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueCluster(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueCluster(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueCluster(obj) },
	})
	namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: filterNamespace,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueNamespace(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueNamespace(obj) },
			DeleteFunc: nil, // Nothing to do.
		},
	})
	c.ddsif = informer.NewDynamicDiscoverySharedInformerFactory(disco, dynClient,
		filterResource,
		informer.GVREventHandlerFuncs{
			AddFunc:    func(gvr schema.GroupVersionResource, obj interface{}) { c.enqueueResource(gvr, obj) },
			UpdateFunc: func(gvr schema.GroupVersionResource, _, obj interface{}) { c.enqueueResource(gvr, obj) },
			DeleteFunc: nil, // Nothing to do.
		}, pollInterval)

	return c
}

type Controller struct {
	resourceQueue  workqueue.RateLimitingInterface
	gvrQueue       workqueue.RateLimitingInterface
	namespaceQueue workqueue.RateLimitingInterface
	clusterQueue   workqueue.RateLimitingInterface

	dynClient       dynamic.Interface
	clusterLister   clusterlisters.ClusterLister
	namespaceLister corelisters.NamespaceLister
	namespaceClient coreclient.NamespaceInterface
	ddsif           informer.DynamicDiscoverySharedInformerFactory
	gvkTrans        *gvk.GVKTranslator

	syncChecks []cache.InformerSynced
}

func filterResource(obj interface{}) bool {
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
	clusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)
	if clusterName != "admin" {
		klog.V(2).Infof("Skipping update for non-admin cluster %q", clusterName)
		return false
	}

	current, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.V(2).Infof("Object was not Unstructured: %T", obj)
		return false
	}

	if namespaceBlocklist.Has(current.GetNamespace()) {
		klog.V(2).Infof("Skipping syncing namespace %q", current.GetNamespace())
		return false
	}
	return true
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
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)
	if clusterName != "admin" {
		klog.Infof("Skipping update for non-admin cluster %q", clusterName)
		return false
	}
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
	c.namespaceQueue.Add(key)
}

func (c *Controller) enqueueCluster(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.clusterQueue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.resourceQueue.ShutDown()
	defer c.gvrQueue.ShutDown()
	defer c.namespaceQueue.ShutDown()
	defer c.clusterQueue.ShutDown()

	klog.Info("Starting Namespace scheduler")
	defer klog.Info("Shutting down Namespace scheduler")

	if !cache.WaitForNamedCacheSync("namespace-scheduler", ctx.Done(), c.syncChecks...) {
		klog.Warning("Failed to wait for caches to sync")
		return
	}

	c.ddsif.Start(ctx)

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startResourceWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startGVRWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startNamespaceWorker(ctx) }, time.Second, ctx.Done())
		go wait.Until(func() { c.startClusterWorker(ctx) }, time.Second, ctx.Done())
	}
	<-ctx.Done()
}

func (c *Controller) startResourceWorker(ctx context.Context) {
	for c.processNextResource(ctx) {
	}
}
func (c *Controller) startGVRWorker(ctx context.Context) {
	for c.processNextGVR(ctx) {
	}
}
func (c *Controller) startNamespaceWorker(ctx context.Context) {
	for c.processNextNamespace(ctx) {
	}
}
func (c *Controller) startClusterWorker(ctx context.Context) {
	for c.processNextCluster(ctx) {
	}
}

func (c *Controller) processNextResource(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.resourceQueue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.resourceQueue.Done(key)

	if err := c.processResource(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.resourceQueue.AddRateLimited(key)
		return true
	}
	c.resourceQueue.Forget(key)
	return true
}

func (c *Controller) processNextGVR(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.gvrQueue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.resourceQueue.Done(key)

	if err := c.processGVR(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.gvrQueue.AddRateLimited(key)
		return true
	}
	c.gvrQueue.Forget(key)
	return true
}

func (c *Controller) processNextNamespace(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.namespaceQueue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.namespaceQueue.Done(key)

	if err := c.processNamespace(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.namespaceQueue.AddRateLimited(key)
		return true
	}
	c.namespaceQueue.Forget(key)
	return true
}

func (c *Controller) processNextCluster(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.clusterQueue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.clusterQueue.Done(key)

	if err := c.processCluster(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.clusterQueue.AddRateLimited(key)
		return true
	}
	c.clusterQueue.Forget(key)
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

	obj, exists, err := c.ddsif.IndexerFor(*gvr).GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		klog.Infof("object %q does not exist", key)
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
	return c.reconcileGVR(ctx, *gvr)
}

// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
var namespaceBlocklist = sets.NewString("kube-system", "kube-public", "kube-node-lease")

func (c *Controller) processNamespace(ctx context.Context, key string) error {
	ns, err := c.namespaceLister.Get(key)
	if k8serrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	// Get logical cluster name.
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split key %q, dropping: %v", key, err)
		return nil
	}
	lclusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)

	return c.reconcileNamespace(ctx, lclusterName, ns.DeepCopy())
}

func (c *Controller) processCluster(ctx context.Context, key string) error {
	cluster, err := c.clusterLister.Get(key)
	if k8serrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	return c.observeCluster(ctx, cluster.DeepCopy())
}
