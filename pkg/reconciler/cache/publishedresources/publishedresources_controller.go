/*
Copyright 2024 The KCP Authors.

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

package publishedresources

import (
	"context"
	"fmt"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/client-go/dynamic"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	cachev1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/cache/v1alpha1"
	cacheinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/cache/v1alpha1"
	cachev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/cache/v1alpha1"
)

const (
	ControllerName = "kcp-published-resources-controller"
)

// NewController returns a new controller for PublishedResource objects.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	dynamicClient dynamic.ClusterInterface,
	publishedResourceInformer cacheinformers.PublishedResourceClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		kcpClient:                kcpClusterClient,
		dynamicClient:            dynamicClient,
		publishedResourceLister:  publishedResourceInformer.Lister(),
		publishedResourceIndexer: publishedResourceInformer.Informer().GetIndexer(),
		commit:                   committer.NewCommitter[*cachev1alpha1.PublishedResource, cachev1alpha1client.PublishedResourceInterface, *cachev1alpha1.PublishedResourceSpec, *cachev1alpha1.PublishedResourceStatus](kcpClusterClient.CacheV1alpha1().PublishedResources()),
	}

	_, _ = publishedResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

type publishedResourceResource = committer.Resource[*cachev1alpha1.PublishedResourceSpec, *cachev1alpha1.PublishedResourceStatus]

type Controller struct {
	queue     workqueue.TypedRateLimitingInterface[string]
	kcpClient kcpclientset.ClusterInterface

	dynamicClient dynamic.ClusterInterface

	publishedResourceIndexer cache.Indexer
	publishedResourceLister  cachev1alpha1listers.PublishedResourceClusterLister

	commit func(ctx context.Context, new, old *publishedResourceResource) error
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing PublishedResource")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}
	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return false, err
	}

	publishedResource, err := c.publishedResourceLister.Cluster(parent).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := publishedResource
	publishedResource = publishedResource.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), publishedResource)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, parent, publishedResource)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &publishedResourceResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &publishedResourceResource{ObjectMeta: publishedResource.ObjectMeta, Spec: &publishedResource.Spec, Status: &publishedResource.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
