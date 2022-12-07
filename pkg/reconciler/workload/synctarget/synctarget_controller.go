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

package synctarget

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const ControllerName = "kcp-synctarget-controller"

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	workspaceShardInformer tenancyv1alpha1informers.ClusterWorkspaceShardClusterInformer,
) *Controller {

	c := &Controller{
		queue:                workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		kcpClusterClient:     kcpClusterClient,
		syncTargetIndexer:    syncTargetInformer.Informer().GetIndexer(),
		workspaceShardLister: workspaceShardInformer.Lister(),
	}

	// Watch for events related to SyncTargets
	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueSyncTarget(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueSyncTarget(obj) },
		DeleteFunc: func(obj interface{}) {},
	})

	// Watch for events related to workspaceShards
	workspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
	})

	return c
}

type Controller struct {
	queue            workqueue.RateLimitingInterface
	kcpClusterClient kcpclientset.ClusterInterface

	workspaceShardLister tenancyv1alpha1listers.ClusterWorkspaceShardClusterLister
	syncTargetIndexer    cache.Indexer
}

func (c *Controller) enqueueSyncTarget(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing SyncTarget")
	c.queue.Add(key)
}

// On workspaceShard changes, enqueue all the syncTargets.
func (c *Controller) enqueueWorkspaceShard(obj interface{}) {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*tenancyv1alpha1.ClusterWorkspaceShard))
	for _, syncTarget := range c.syncTargetIndexer.List() {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(syncTarget)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing SyncTarget because of ClusterWorkspaceShard")
		c.queue.Add(key)
	}
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
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
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	obj, exists, err := c.syncTargetIndexer.GetByKey(key)
	if err != nil {
		logger.Error(err, "failed to get SyncTarget")
		return nil
	}

	if !exists {
		logger.Info("syncTarget was deleted")
		return nil
	}

	currentSyncTarget := obj.(*workloadv1alpha1.SyncTarget)
	logger = logging.WithObject(klog.FromContext(ctx), currentSyncTarget)
	ctx = klog.NewContext(ctx, logger)

	workspacesShards, err := c.workspaceShardLister.List(labels.Everything())
	if err != nil {
		return err
	}

	newSyncTarget, err := c.reconcile(ctx, currentSyncTarget, workspacesShards)
	if err != nil {
		logger.Error(err, "failed to reconcile syncTarget")
		return err
	}

	if reflect.DeepEqual(currentSyncTarget, newSyncTarget) {
		return nil
	}

	currentSyncTargetJSON, err := json.Marshal(currentSyncTarget)
	if err != nil {
		logger.Error(err, "failed to marshal syncTarget")
		return err
	}
	newSyncTargetJSON, err := json.Marshal(newSyncTarget)
	if err != nil {
		logger.Error(err, "failed to marshal syncTarget")
		return err
	}

	patchBytes, err := jsonpatch.CreateMergePatch(currentSyncTargetJSON, newSyncTargetJSON)
	if err != nil {
		logger.Error(err, "failed to create merge patch for syncTarget")
		return err
	}

	if !reflect.DeepEqual(currentSyncTarget.ObjectMeta, newSyncTarget.ObjectMeta) || !reflect.DeepEqual(currentSyncTarget.Spec, newSyncTarget.Spec) {
		logger.WithValues("patch", string(patchBytes)).V(2).Info("patching SyncTarget")
		if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(currentSyncTarget).Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, currentSyncTarget.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			logger.Error(err, "failed to patch sync target")
			return err
		}
	}

	if !reflect.DeepEqual(currentSyncTarget.Status, newSyncTarget.Status) {
		logger.WithValues("patch", string(patchBytes)).V(2).Info("patching SyncTarget status")
		if _, err := c.kcpClusterClient.Cluster(logicalcluster.From(currentSyncTarget).Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, currentSyncTarget.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status"); err != nil {
			logger.Error(err, "failed to patch sync target status")
			return err
		}
	}

	return nil
}
