/*
Copyright 2022 The KCP Authors.

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

package placement

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	schedulingv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-workload-placement"
)

// NewController returns a new controller starting the process of selecting synctarget for a placement
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	locationInformer schedulingv1alpha1informers.LocationClusterInformer,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	placementInformer schedulingv1alpha1informers.PlacementClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,

		kcpClusterClient: kcpClusterClient,

		locationLister: locationInformer.Lister(),

		syncTargetLister: syncTargetInformer.Lister(),

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),
	}

	indexers.AddIfNotPresentOrDie(
		c.placementIndexer,
		cache.Indexers{
			indexers.PlacementBySelectedLocationWorkspace: indexers.IndexPlacementBySelectedLocationWorkspace,
		},
	)

	locationInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueueLocation,
			UpdateFunc: func(old, obj interface{}) {
				oldLoc := old.(*schedulingv1alpha1.Location)
				newLoc := obj.(*schedulingv1alpha1.Location)
				if !reflect.DeepEqual(oldLoc.Spec, newLoc.Spec) || !reflect.DeepEqual(oldLoc.Labels, newLoc.Labels) {
					c.enqueueLocation(obj)
				}
			},
			DeleteFunc: c.enqueueLocation,
		},
	)

	syncTargetInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueueSyncTarget,
			UpdateFunc: func(old, obj interface{}) {
				oldCluster := old.(*workloadv1alpha1.SyncTarget)
				oldClusterCopy := *oldCluster

				// ignore fields that scheduler does not care
				oldClusterCopy.ResourceVersion = "0"
				oldClusterCopy.Status.LastSyncerHeartbeatTime = nil
				oldClusterCopy.Status.VirtualWorkspaces = nil
				oldClusterCopy.Status.Capacity = nil

				newCluster := obj.(*workloadv1alpha1.SyncTarget)
				newClusterCopy := *newCluster
				newClusterCopy.ResourceVersion = "0"
				newClusterCopy.Status.LastSyncerHeartbeatTime = nil
				newClusterCopy.Status.VirtualWorkspaces = nil
				newClusterCopy.Status.Capacity = nil

				// compare ignoring heart-beat
				if !reflect.DeepEqual(oldClusterCopy, newClusterCopy) {
					c.enqueueSyncTarget(obj)
				}
			},
			DeleteFunc: c.enqueueSyncTarget,
		},
	)

	logger := logging.WithReconciler(klog.Background(), ControllerName)
	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueuePlacement(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueuePlacement(obj, logger, "") },
	})

	return c, nil
}

// controller
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	locationLister schedulingv1alpha1listers.LocationClusterLister

	syncTargetLister workloadv1alpha1listers.SyncTargetClusterLister

	placementLister  schedulingv1alpha1listers.PlacementClusterLister
	placementIndexer cache.Indexer
}

// enqueueLocation finds placement ref to this location at first, and then namespaces bound to this placement.
func (c *controller) enqueueLocation(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	placements, err := indexers.ByIndex[*schedulingv1alpha1.Placement](c.placementIndexer, indexers.PlacementBySelectedLocationWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*schedulingv1alpha1.Location))
	for _, placement := range placements {
		c.enqueuePlacement(placement, logger, " because of Location")
	}
}

func (c *controller) enqueuePlacement(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info(fmt.Sprintf("queueing Placement%s", logSuffix))
	c.queue.Add(key)
}

func (c *controller) enqueueSyncTarget(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	placements, err := indexers.ByIndex[*schedulingv1alpha1.Placement](c.placementIndexer, indexers.PlacementBySelectedLocationWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*workloadv1alpha1.SyncTarget))
	for _, placement := range placements {
		c.enqueuePlacement(placement, logger, " because of SyncTarget")
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	obj, err := c.placementLister.Cluster(clusterName).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	reconcileErr := c.reconcile(ctx, obj)

	return reconcileErr
}
