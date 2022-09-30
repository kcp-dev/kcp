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

package basecontroller

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// ClusterReconcileImpl defines the methods that ClusterReconciler
// will call in response to changes to Cluster resources.
type ClusterReconcileImpl interface {
	Reconcile(ctx context.Context, cluster *workloadv1alpha1.SyncTarget) error
	Cleanup(ctx context.Context, deletedCluster *workloadv1alpha1.SyncTarget)
}

type ClusterQueue interface {
	EnqueueAfter(*workloadv1alpha1.SyncTarget, time.Duration)
}

// NewClusterReconciler returns a new controller which reconciles
// Cluster resources in the API server it reaches using the REST
// client.
func NewClusterReconciler(
	name string,
	reconciler ClusterReconcileImpl,
	kcpClusterClient kcpclient.Interface,
	clusterInformer workloadinformers.SyncTargetInformer,
) (*ClusterReconciler, ClusterQueue, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name)

	c := &ClusterReconciler{
		name:             name,
		reconciler:       reconciler,
		kcpClusterClient: kcpClusterClient,
		clusterIndexer:   clusterInformer.Informer().GetIndexer(),
		queue:            queue,
	}

	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.deletedCluster(obj) },
	})

	return c, queueAdapter{queue}, nil
}

type queueAdapter struct {
	queue interface {
		AddAfter(item interface{}, duration time.Duration)
	}
}

func (a queueAdapter) EnqueueAfter(cl *workloadv1alpha1.SyncTarget, dur time.Duration) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(cl)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	a.queue.AddAfter(key, dur)
}

type ClusterReconciler struct {
	name             string
	reconciler       ClusterReconcileImpl
	kcpClusterClient kcpclient.Interface
	clusterIndexer   cache.Indexer

	queue workqueue.RateLimitingInterface
}

func (c *ClusterReconciler) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), c.name), key)
	logger.V(2).Info("queueing SyncTarget")
	c.queue.Add(key)
}

func (c *ClusterReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *ClusterReconciler) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.name)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.Until(func() { c.startWorker(ctx) }, time.Millisecond*10, ctx.Done())

	<-ctx.Done()
}

func (c *ClusterReconciler) ShutDown() {
	c.queue.ShutDown()
}

func (c *ClusterReconciler) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", c.name, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *ClusterReconciler) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	obj, exists, err := c.clusterIndexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		logger.Info("object for key was deleted")
		return nil
	}
	current := obj.(*workloadv1alpha1.SyncTarget).DeepCopy()
	previous := current.DeepCopy()

	logger = logging.WithObject(logger, previous)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconciler.Reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, current.Status) {
		_, uerr := c.kcpClusterClient.WorkloadV1alpha1().SyncTargets().UpdateStatus(logicalcluster.WithCluster(ctx, logicalcluster.From(current)), current, metav1.UpdateOptions{})
		return uerr
	}

	return nil
}

func (c *ClusterReconciler) deletedCluster(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), c.name)

	castObj, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			logger.Error(fmt.Errorf("unexpected tombstone %T, not %T", obj, cache.DeletedFinalStateUnknown{}), "couldn't get object from tombstone")
			return
		}
		castObj, ok = tombstone.Obj.(*workloadv1alpha1.SyncTarget)
		if !ok {
			logger.Error(fmt.Errorf("unexpected tombstone %T, not %T", obj, &workloadv1alpha1.SyncTarget{}), "couldn't get object from tombstone")
			return
		}
	}
	logger = logging.WithObject(logger, castObj)
	ctx := klog.NewContext(context.TODO(), logger)
	logger.V(4).Info("responding to deletion of SyncTarget")
	c.reconciler.Cleanup(ctx, castObj)
}
