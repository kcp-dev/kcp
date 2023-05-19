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

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	corev1listers "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	schedulingv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/scheduling/v1alpha1"
	schedulingv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/scheduling/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/scheduling/v1alpha1"
)

const (
	ControllerName      = "kcp-scheduling-placement"
	byLocationWorkspace = ControllerName + "-byLocationWorkspace"
)

// NewController returns a new controller placing namespaces onto locations by create
// a placement annotation..
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	locationInformer, globalLocationInformer schedulingv1alpha1informers.LocationClusterInformer,
	placementInformer schedulingv1alpha1informers.PlacementClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(ns *corev1.Namespace, duration time.Duration) {
			key, err := kcpcache.MetaClusterNamespaceKeyFunc(ns)
			if err != nil {
				runtime.HandleError(err)
				return
			}
			queue.AddAfter(key, duration)
		},
		kcpClusterClient: kcpClusterClient,

		namespaceLister: namespaceInformer.Lister(),

		listLocationsByPath: func(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error) {
			objs, err := indexers.ByIndexWithFallback[*schedulingv1alpha1.Location](locationInformer.Informer().GetIndexer(), globalLocationInformer.Informer().GetIndexer(), indexers.ByLogicalClusterPath, path.String())
			if err != nil {
				return nil, err
			}
			return objs, nil
		},

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),

		commit: committer.NewCommitter[*Placement, Patcher, *PlacementSpec, *PlacementStatus](kcpClusterClient.SchedulingV1alpha1().Placements()),
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		byLocationWorkspace: indexByLocationWorkspace,
	}); err != nil {
		return nil, err
	}

	indexers.AddIfNotPresentOrDie(locationInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPath: indexers.IndexByLogicalClusterPath,
	})
	indexers.AddIfNotPresentOrDie(globalLocationInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPath: indexers.IndexByLogicalClusterPath,
	})

	// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
	var namespaceBlocklist = sets.New[string]("kube-system", "kube-public", "kube-node-lease")
	_, _ = namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch ns := obj.(type) {
			case *corev1.Namespace:
				return !namespaceBlocklist.Has(ns.Name)
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueueNamespace,
			UpdateFunc: func(old, obj interface{}) {
				oldNs := old.(*corev1.Namespace)
				newNs := obj.(*corev1.Namespace)

				if !reflect.DeepEqual(oldNs.Annotations, newNs.Annotations) {
					c.enqueueNamespace(obj)
				}
			},
			DeleteFunc: c.enqueueNamespace,
		},
	})

	_, _ = locationInformer.Informer().AddEventHandler(
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

	_, _ = placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueuePlacement,
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj) },
		DeleteFunc: c.enqueuePlacement,
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
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*corev1.Namespace, time.Duration)

	kcpClusterClient kcpclientset.ClusterInterface

	namespaceLister corev1listers.NamespaceClusterLister

	listLocationsByPath func(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error)

	placementLister  schedulingv1alpha1listers.PlacementClusterLister
	placementIndexer cache.Indexer

	commit CommitFunc
}

func (c *controller) enqueuePlacement(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing Placement")
	c.queue.Add(key)
}

// enqueueNamespace enqueues all placements for the namespace.
func (c *controller) enqueueNamespace(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
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

	for _, placement := range placements {
		namespaceKey := key
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(placement)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Namespace changed", "Namespace", namespaceKey)
		c.queue.Add(key)
	}
}

func (c *controller) enqueueLocation(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}

	location, ok := obj.(*schedulingv1alpha1.Location)
	if !ok {
		runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
		return
	}

	// placements referencing by cluster name
	placements, err := c.placementIndexer.ByIndex(byLocationWorkspace, logicalcluster.From(location).String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if path := location.Annotations[core.LogicalClusterPathAnnotationKey]; path != "" {
		// placements referencing by path
		placementsByPath, err := c.placementIndexer.ByIndex(byLocationWorkspace, path)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		placements = append(placements, placementsByPath...)
	}

	for _, obj := range placements {
		placement := obj.(*schedulingv1alpha1.Placement)
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(placement)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Location changed")
		c.queue.Add(key)
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
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.placementLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
