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
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	controllerName      = "kcp-scheduling-placement"
	byWorkspace         = controllerName + "-byWorkspace" // will go away with scoping
	byLocationWorkspace = controllerName + "-byLoactionWorkspace"
)

// NewController returns a new controller placing namespaces onto locations by create
// a placement annotation..
func NewController(
	kcpClusterClient kcpclient.Interface,
	namespaceInformer coreinformers.NamespaceInformer,
	locationInformer schedulinginformers.LocationInformer,
	placementInformer schedulinginformers.PlacementInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(ns *corev1.Namespace, duration time.Duration) {
			key := clusters.ToClusterAwareKey(logicalcluster.From(ns), ns.Name)
			queue.AddAfter(key, duration)
		},
		kcpClusterClient: kcpClusterClient,

		namespaceLister:  namespaceInformer.Lister(),
		namespaceIndexer: namespaceInformer.Informer().GetIndexer(),

		locationLister:  locationInformer.Lister(),
		locationIndexer: locationInformer.Informer().GetIndexer(),

		placementLister:  placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),
	}

	if err := locationInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace:         indexByWorkspace,
		byLocationWorkspace: indexByLocationWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := namespaceInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	// namespaceBlocklist holds a set of namespaces that should never be synced from kcp to physical clusters.
	var namespaceBlocklist = sets.NewString("kube-system", "kube-public", "kube-node-lease")
	namespaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
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

	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueuePlacement,
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj) },
		DeleteFunc: c.enqueuePlacement,
	})

	return c, nil
}

// controller
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*corev1.Namespace, time.Duration)

	kcpClusterClient kcpclient.Interface

	namespaceLister  corelisters.NamespaceLister
	namespaceIndexer cache.Indexer

	locationLister  schedulinglisters.LocationLister
	locationIndexer cache.Indexer

	placementLister  schedulinglisters.PlacementLister
	placementIndexer cache.Indexer
}

func (c *controller) enqueuePlacement(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logger.V(2).Info("queueing Placement")
	c.queue.Add(key)
}

// enqueueNamespace enqueues all placements for the namespace.
func (c *controller) enqueueNamespace(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), controllerName)
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _ := clusters.SplitClusterAwareKey(key)

	placements, err := c.placementIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range placements {
		placement := obj.(*schedulingv1alpha1.Placement)
		namespaceKey := key
		key := clusters.ToClusterAwareKey(logicalcluster.From(placement), placement.Name)
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Namespace changed", "Namespace", namespaceKey)
		c.queue.Add(key)
	}
}

func (c *controller) enqueueLocation(obj interface{}) {
	logger := logging.WithReconciler(klog.Background(), controllerName)
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _ := clusters.SplitClusterAwareKey(key)

	placements, err := c.placementIndexer.ByIndex(byLocationWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range placements {
		placement := obj.(*schedulingv1alpha1.Placement)
		locationKey := key
		key := clusters.ToClusterAwareKey(logicalcluster.From(placement), placement.Name)
		logging.WithQueueKey(logger, key).V(2).Info("queueing Placement because Location changed", "Location", locationKey)
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.placementLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	reconcileErr := c.reconcile(ctx, obj)

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(schedulingv1alpha1.Placement{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for placement %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(schedulingv1alpha1.Placement{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for LocationDomain %s|%s: %w", clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for LocationDomain %s|%s: %w", clusterName, name, err)
		}
		logger.V(2).Info("patching placement", "patch", string(patchBytes))
		_, uerr := c.kcpClusterClient.SchedulingV1alpha1().Placements().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return reconcileErr
}
