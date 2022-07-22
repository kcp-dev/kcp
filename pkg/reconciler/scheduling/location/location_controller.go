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

package location

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
)

const (
	controllerName = "kcp-scheduling-location-status"
	byWorkspace    = controllerName + "-byWorkspace" // will go away with scoping
)

// NewController returns a new controller reconciling location status.
func NewController(
	kcpClusterClient kcpclient.Interface,
	locationInformer schedulinginformers.LocationInformer,
	syncTargetInformer workloadinformers.SyncTargetInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(location *schedulingv1alpha1.Location, duration time.Duration) {
			key := clusters.ToClusterAwareKey(logicalcluster.From(location), location.Name)
			queue.AddAfter(key, duration)
		},
		kcpClusterClient:  kcpClusterClient,
		locationLister:    locationInformer.Lister(),
		locationIndexer:   locationInformer.Informer().GetIndexer(),
		syncTargetLister:  syncTargetInformer.Lister(),
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := locationInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	locationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueLocation(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueLocation(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueLocation(obj) },
	})

	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueSyncTarget(obj) },
		UpdateFunc: func(old, obj interface{}) {
			oldCluster, ok := old.(*workloadv1alpha1.SyncTarget)
			if !ok {
				return
			}
			objCluster, ok := obj.(*workloadv1alpha1.SyncTarget)
			if !ok {
				return
			}

			// only enqueue if spec or conditions change.
			oldCluster = oldCluster.DeepCopy()
			oldCluster.Status.Allocatable = objCluster.Status.Allocatable
			oldCluster.Status.Capacity = objCluster.Status.Capacity
			oldCluster.Status.LastSyncerHeartbeatTime = objCluster.Status.LastSyncerHeartbeatTime

			if !equality.Semantic.DeepEqual(oldCluster, objCluster) {
				c.enqueueSyncTarget(obj)
			}
		},
		DeleteFunc: func(obj interface{}) { c.enqueueSyncTarget(obj) },
	})

	return c, nil
}

// controller
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*schedulingv1alpha1.Location, time.Duration)

	kcpClusterClient kcpclient.Interface

	locationLister    schedulinglisters.LocationLister
	locationIndexer   cache.Indexer
	syncTargetLister  workloadlisters.SyncTargetLister
	syncTargetIndexer cache.Indexer
}

func (c *controller) enqueueLocation(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Queueing Location %q", key)
	c.queue.Add(key)
}

// enqueueSyncTarget maps a SyncTarget to LocationDomain for enqueuing.
func (c *controller) enqueueSyncTarget(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	lcluster, _ := clusters.SplitClusterAwareKey(key)
	domains, err := c.locationIndexer.ByIndex(byWorkspace, lcluster.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, domain := range domains {
		c.enqueueLocation(domain)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting down %s controller", controllerName)

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
	namespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.locationLister.Get(key)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(schedulingv1alpha1.Location{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(schedulingv1alpha1.Location{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.SchedulingV1alpha1().Locations().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}
