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
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	schedulingv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/scheduling/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	schedulinginformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/scheduling/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/workload/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/sdk/client/listers/apis/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/scheduling/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/workload/v1alpha1"
)

const (
	ControllerName         = "kcp-workload-placement"
	bySelectedLocationPath = ControllerName + "-bySelectedLocationPath"
)

// NewController returns a new controller starting the process of selecting synctarget for a placement.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	locationInformer, globalLocationInformer schedulinginformers.LocationClusterInformer,
	syncTargetInformer, globalSyncTargetInformer workloadinformers.SyncTargetClusterInformer,
	placementInformer schedulinginformers.PlacementClusterInformer,
	apiBindingInformer apisinformers.APIBindingClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,

		kcpClusterClient: kcpClusterClient,

		logicalClusterLister: logicalClusterInformer.Lister(),

		locationLister:  locationInformer.Lister(),
		locationIndexer: locationInformer.Informer().GetIndexer(),

		listLocations: func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error) {
			locations, err := locationInformer.Lister().Cluster(clusterName).List(labels.Everything())
			if err != nil || len(locations) == 0 {
				return globalLocationInformer.Lister().Cluster(clusterName).List(labels.Everything())
			}

			return locations, nil
		},

		syncTargetLister: syncTargetInformer.Lister(),

		listSyncTargets: func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
			targets, err := syncTargetInformer.Lister().Cluster(clusterName).List(labels.Everything())
			if err != nil || len(targets) == 0 {
				return globalSyncTargetInformer.Lister().Cluster(clusterName).List(labels.Everything())
			}
			return targets, nil
		},

		getLocation: func(path logicalcluster.Path, name string) (*schedulingv1alpha1.Location, error) {
			return indexers.ByPathAndNameWithFallback[*schedulingv1alpha1.Location](schedulingv1alpha1.Resource("locations"), locationInformer.Informer().GetIndexer(), globalLocationInformer.Informer().GetIndexer(), path, name)
		},

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),

		apiBindingLister: apiBindingInformer.Lister(),

		commit: committer.NewCommitter[*Placement, Patcher, *PlacementSpec, *PlacementStatus](kcpClusterClient.SchedulingV1alpha1().Placements()),
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		bySelectedLocationPath: indexBySelectedLocationPath,
	}); err != nil {
		return nil, err
	}

	indexers.AddIfNotPresentOrDie(locationInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(globalLocationInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	locationInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.enqueueLocation(obj, logger) },
			UpdateFunc: func(old, obj interface{}) {
				oldLoc := old.(*schedulingv1alpha1.Location)
				newLoc := obj.(*schedulingv1alpha1.Location)
				if !reflect.DeepEqual(oldLoc.Spec, newLoc.Spec) || !reflect.DeepEqual(oldLoc.Labels, newLoc.Labels) {
					c.enqueueLocation(obj, logger)
				}
			},
			DeleteFunc: func(obj interface{}) { c.enqueueLocation(obj, logger) },
		},
	)

	syncTargetInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
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
					c.enqueueSyncTarget(obj, logger)
				}
			},
			DeleteFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
		},
	)

	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueuePlacement(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj, logger) },
		DeleteFunc: func(obj interface{}) { c.enqueuePlacement(obj, logger) },
	})

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj, logger) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
	})

	return c, nil
}

type Placement = schedulingv1alpha1.Placement
type PlacementSpec = schedulingv1alpha1.PlacementSpec
type PlacementStatus = schedulingv1alpha1.PlacementStatus
type Patcher = schedulingv1alpha1client.PlacementInterface
type Resource = committer.Resource[*PlacementSpec, *PlacementStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	locationLister  schedulingv1alpha1listers.LocationClusterLister
	locationIndexer cache.Indexer

	listLocations func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error)
	getLocation   func(path logicalcluster.Path, name string) (*schedulingv1alpha1.Location, error)

	syncTargetLister workloadv1alpha1listers.SyncTargetClusterLister

	listSyncTargets func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)

	placementLister  schedulingv1alpha1listers.PlacementClusterLister
	placementIndexer cache.Indexer

	apiBindingLister apislisters.APIBindingClusterLister
	commit           CommitFunc
}

// enqueueLocation finds placement ref to this location at first, and then namespaces bound to this placement.
func (c *controller) enqueueLocation(obj interface{}, logger logr.Logger) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}

	location, ok := obj.(*schedulingv1alpha1.Location)
	if !ok {
		runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
		return
	}

	// placements referencing by cluster name
	placements, err := c.placementIndexer.ByIndex(bySelectedLocationPath, logicalcluster.From(location).String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if path := location.Annotations[core.LogicalClusterPathAnnotationKey]; path != "" {
		// placements referencing by path
		placementsByPath, err := c.placementIndexer.ByIndex(bySelectedLocationPath, path)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		placements = append(placements, placementsByPath...)
	}

	logger = logger.WithValues(logging.FromPrefix("locationReason", location)...)
	for _, placement := range placements {
		c.enqueuePlacement(placement, logger)
	}
}

func (c *controller) enqueuePlacement(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing Placement")
	c.queue.Add(key)
}

func (c *controller) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
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

	placements, err := c.placementLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger = logger.WithValues(logging.FromPrefix("apiBindingReason", obj.(*apisv1alpha1.APIBinding))...)

	for _, placement := range placements {
		c.enqueuePlacement(placement, logger)
	}
}

func (c *controller) enqueueSyncTarget(obj interface{}, logger logr.Logger) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}

	syncTarget, ok := obj.(*workloadv1alpha1.SyncTarget)
	if !ok {
		runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
		return
	}

	// Get all locations in the same cluster and enqueue locations.
	locations, err := c.listLocations(logicalcluster.From(syncTarget))
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger = logger.WithValues(logging.FromPrefix("syncTargetReason", syncTarget)...)

	for _, location := range locations {
		c.enqueueLocation(location, logger)
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

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
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

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false, nil
	}
	obj, err := c.placementLister.Cluster(clusterName).Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}
	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, obj)
	if err != nil {
		errs = append(errs, err)
	}

	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
