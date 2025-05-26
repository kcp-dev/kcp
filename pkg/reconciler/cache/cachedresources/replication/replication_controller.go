/*
Copyright 2025 The KCP Authors.

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

package replication

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

// Locally we store object with the original form of the object.
// In the cache we store object with the CachedResource as a wrapper, and store the original GVR in the labels.
// This way we can extract the original GVR from the CachedResource and use it to enqueue the object.

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-published-resource-replication-controller"
)

// NewController returns a new replication controller.
func NewController(
	shardName string,
	dynamicCacheClient kcpdynamic.ClusterInterface,
	kcpCacheClient kcpclientset.ClusterInterface,
	gvr schema.GroupVersionResource,
	replicated *ReplicatedGVR,
	callback func(),
	localLabelSelector labels.Selector,
) (*Controller, error) {
	c := &Controller{
		shardName: shardName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		dynamicCacheClient: dynamicCacheClient,
		kcpCacheClient:     kcpCacheClient,
		replicated:         replicated,
		gvr:                gvr,
		callback:           callback,
		cleanupFuncs:       make([]func(), 0),
		localLabelSelector: localLabelSelector,
	}

	localHandler, err := c.replicated.Local.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueObject(obj, c.gvr) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, c.gvr) },
		DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, c.gvr) },
	})
	if err != nil {
		return nil, err
	}
	c.cleanupFuncs = append(c.cleanupFuncs, func() {
		_ = c.replicated.Local.RemoveEventHandler(localHandler)
	})

	globalHandler, err := c.replicated.Global.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueCacheObject(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueCacheObject(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueCacheObject(obj) },
	})
	if err != nil {
		return nil, err
	}
	c.cleanupFuncs = append(c.cleanupFuncs, func() {
		_ = c.replicated.Global.RemoveEventHandler(globalHandler)
	})

	return c, nil
}

func (c *Controller) enqueueObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

func (c *Controller) enqueueCacheObject(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	// This way we extract what is the original GVR of the object that we are replicating.
	cr, ok := obj.(*cachev1alpha1.CachedObject)
	if !ok {
		utilruntime.HandleError(fmt.Errorf("expected *cachev1alpha1.CachedObject, got %T", obj))
		return
	}

	labels := cr.GetLabels()
	gvr := schema.GroupVersionResource{
		Group:    labels[LabelKeyObjectGroup],
		Version:  labels[LabelKeyObjectVersion],
		Resource: labels[LabelKeyObjectResource],
	}

	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer func() {
		for _, cleanupFunc := range c.cleanupFuncs {
			cleanupFunc()
		}
	}()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	c.started = true
	<-ctx.Done()
	c.started = false
}

func (c *Controller) Started() bool {
	return c.started
}

func (c *Controller) SetLabelSelector(localLabelSelector labels.Selector) {
	c.localLabelSelector = localLabelSelector
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	gvrKey, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(gvrKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), gvrKey)
	ctx = klog.NewContext(ctx, logger)
	err := c.reconcile(ctx, gvrKey)
	if err == nil {
		c.queue.Forget(gvrKey)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with: %w", gvrKey, err))
	c.queue.AddRateLimited(gvrKey)

	return true
}

func (c *Controller) SetDeleted(ctx context.Context) {
	c.deleted = true
}

type Controller struct {
	shardName string
	queue     workqueue.TypedRateLimitingInterface[string]

	dynamicCacheClient kcpdynamic.ClusterInterface
	kcpCacheClient     kcpclientset.ClusterInterface

	replicated *ReplicatedGVR
	gvr        schema.GroupVersionResource

	// callback is called when we want to trigger parent object reconciliation.
	// Cache state is being managed by child controller, so we need to trigger parent object reconciliation
	// to update parent object status.
	// Example:
	// 1. Add new object to replicate, so GVR controller picks it up.
	// 2. Object is replicated to cache by child controller.
	// 3. callback is called to update parent object status.
	callback func()
	// cleanupFuncs are cleanup functions that are called when the controller is stopped.
	cleanupFuncs []func()

	// localLabelSelector is the label selector that we use to filter the objects that we want to replicate.
	// It is set when the controller is created and can be changed by the parent controller.
	localLabelSelector labels.Selector

	started bool
	deleted bool
}

type ReplicatedGVR struct {
	Kind          string
	Filter        func(u *unstructured.Unstructured) bool
	Global, Local cache.SharedIndexInformer
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(replicated *ReplicatedGVR) {
	indexers.AddIfNotPresentOrDie(
		replicated.Global.GetIndexer(),
		cache.Indexers{
			ByGVRAndShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)
}
