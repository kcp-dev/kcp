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
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
)

const (
	controllerName      = "kcp-namespace-scheduling-placement"
	byWorkspace         = controllerName + "-byWorkspace" // will go away with scoping
	byLocationWorkspace = controllerName + "-byLocationWorkspace"
)

// NewController returns a new controller starting the process of placing namespaces onto locations by creating
// a placement annotation.
func NewController(
	kubeClusterClient kubernetesclient.Interface,
	namespaceInformer coreinformers.NamespaceInformer,
	locationInformer schedulinginformers.LocationInformer,
	syncTargetInformer workloadinformers.SyncTargetInformer,
	placementInformer schedulinginformers.PlacementInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(ns *corev1.Namespace, duration time.Duration) {
			key := clusters.ToClusterAwareKey(logicalcluster.From(ns), ns.Name)
			queue.AddAfter(key, duration)
		},

		kubeClusterClient: kubeClusterClient,

		namespaceLister:  namespaceInformer.Lister(),
		namespaceIndexer: namespaceInformer.Informer().GetIndexer(),

		locationLister:  locationInformer.Lister(),
		locationIndexer: locationInformer.Informer().GetIndexer(),

		syncTargetLister:  syncTargetInformer.Lister(),
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),

		placmentLister:   placementInformer.Lister(),
		placementIndexer: placementInformer.Informer().GetIndexer(),
	}

	if err := locationInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorksapce,
	}); err != nil {
		return nil, err
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorksapce,
	}); err != nil {
		return nil, err
	}

	if err := namespaceInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorksapce,
	}); err != nil {
		return nil, err
	}

	if err := placementInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace:         indexByWorksapce,
		byLocationWorkspace: indexByLoactionWorkspace,
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
			AddFunc:    c.enqueueNamespace,
			UpdateFunc: func(_, obj interface{}) { c.enqueueNamespace(obj) },
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

	placementInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueuePlacement(obj, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueuePlacement(obj, "") },
		DeleteFunc: func(obj interface{}) { c.enqueuePlacement(obj, "") },
	})

	return c, nil
}

// controller
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*corev1.Namespace, time.Duration)

	kubeClusterClient kubernetesclient.Interface

	namespaceLister  corelisters.NamespaceLister
	namespaceIndexer cache.Indexer

	locationLister  schedulinglisters.LocationLister
	locationIndexer cache.Indexer

	syncTargetLister  workloadlisters.SyncTargetLister
	syncTargetIndexer cache.Indexer

	placmentLister   schedulinglisters.PlacementLister
	placementIndexer cache.Indexer
}

func (c *controller) enqueueNamespace(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, name := clusters.SplitClusterAwareKey(key)

	klog.V(2).Infof("Queueing Namespace %s|%s", clusterName.String(), name)
	c.queue.Add(key)
}

// enqueueLocation finds placement ref to this location at first, and then namespaces bound to this placement.
func (c *controller) enqueueLocation(obj interface{}) {
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
		c.enqueuePlacement(obj, fmt.Sprintf(" because of Location %s", key))
	}
}

func (c *controller) enqueuePlacement(obj interface{}, logSuffix string) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, name := clusters.SplitClusterAwareKey(key)

	nss, err := c.namespaceIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, o := range nss {
		ns := o.(*corev1.Namespace)
		nskey := clusters.ToClusterAwareKey(logicalcluster.From(ns), ns.Name)
		klog.V(2).Infof("Queueing namespace %s|%s because of placement %s|%s%s", logicalcluster.From(ns), ns.Name, clusterName, name, logSuffix)
		c.queue.Add(nskey)
	}
}

func (c *controller) enqueueSyncTarget(obj interface{}) {
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
		c.enqueuePlacement(obj, fmt.Sprintf(" because of SyncTarget %s", key))
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
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.namespaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	reconcileErr := c.reconcile(ctx, obj)

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(corev1.Namespace{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for placement %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(corev1.Namespace{
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
		klog.V(2).Infof("Patching namespace %s|%s with patch %s", clusterName, obj.Name, string(patchBytes))
		_, uerr := c.kubeClusterClient.CoreV1().Namespaces().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return reconcileErr
}

func (c *controller) patchNamespace(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	klog.V(2).Infof("Patching namespace %s|%s with patch %s", clusterName, name, string(data))
	return c.kubeClusterClient.CoreV1().Namespaces().Patch(logicalcluster.WithCluster(ctx, clusterName), name, pt, data, opts, subresources...)
}
