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
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	workloadv1alpha1typed "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/workload/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	resyncPeriod   = 10 * time.Hour
	controllerName = "kcp-syncer-synctarget-gvrsource-controller"
)

// NewSyncTargetController returns a controller that watches the [workloadv1alpha1.SyncTarget]
// associated to this syncer.
// It then calls the update methods on the shardManager and gvrSource
// that were passed in arguments, to update available shards and GVRs
// according to the content of the SyncTarget status.
func NewSyncTargetController(
	syncerLogger logr.Logger,
	syncTargetClient workloadv1alpha1typed.SyncTargetInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetInformer,
	syncTargetName string,
	syncTargetClusterName logicalcluster.Name,
	syncTargetUID types.UID,
	gvrSource *syncTargetGVRSource,
	shardManager *shardManager,
) (*controller, error) {
	c := &controller{
		queue:            workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		syncTargetClient: syncTargetClient,
		syncTargetUID:    syncTargetUID,
		syncTargetLister: syncTargetInformer.Lister(),
		gvrSource:        gvrSource,
		shardManager:     shardManager,
	}

	logger := logging.WithReconciler(syncerLogger, controllerName)

	syncTargetInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, name, err := cache.SplitMetaNamespaceKey(key)
			if err != nil {
				return false
			}
			return name == syncTargetName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
			UpdateFunc: func(old, obj interface{}) { c.enqueueSyncTarget(obj, logger) },
			DeleteFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, logger) },
		},
	})

	return c, nil
}

type controller struct {
	queue workqueue.RateLimitingInterface

	syncTargetUID    types.UID
	syncTargetLister workloadv1alpha1listers.SyncTargetLister
	syncTargetClient workloadv1alpha1typed.SyncTargetInterface

	shardManager *shardManager
	gvrSource    *syncTargetGVRSource
}

func (c *controller) enqueueSyncTarget(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing SyncTarget")

	c.queue.Add(key)
}

// Start starts the controller worker.
func (c *controller) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)

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
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}

	syncTarget, err := c.syncTargetLister.Get(name)
	if apierrors.IsNotFound(err) {
		c.shardManager.updateShards(ctx, nil)
		c.gvrSource.updateGVRs(ctx, nil)
		return nil
	}
	if err != nil {
		return err
	}
	if syncTarget.GetUID() != c.syncTargetUID {
		c.shardManager.updateShards(ctx, nil)
		c.gvrSource.updateGVRs(ctx, nil)
		return nil
	}

	c.shardManager.updateShards(ctx, syncTarget)
	unauthorizedGVRs, errs := c.gvrSource.updateGVRs(ctx, syncTarget)
	if err != nil {
		return err
	}

	newSyncTarget := syncTarget.DeepCopy()
	if len(unauthorizedGVRs) > 0 {
		conditions.MarkFalse(
			newSyncTarget,
			workloadv1alpha1.SyncerAuthorized,
			"SyncerUnauthorized",
			conditionsv1alpha1.ConditionSeverityError,
			"SSAR check failed for gvrs: %s", strings.Join(unauthorizedGVRs, ";"),
		)
	} else {
		conditions.MarkTrue(newSyncTarget, workloadv1alpha1.SyncerAuthorized)
	}

	if err := c.patchSyncTargetCondition(ctx, newSyncTarget, syncTarget); err != nil {
		errs = append(errs, err)
	}

	return errors.NewAggregate(errs)
}

func (c *controller) patchSyncTargetCondition(ctx context.Context, new, old *workloadv1alpha1.SyncTarget) error {
	logger := klog.FromContext(ctx)
	// If the object being reconciled changed as a result, update it.
	if equality.Semantic.DeepEqual(old.Status.Conditions, new.Status.Conditions) {
		return nil
	}
	oldData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		Status: workloadv1alpha1.SyncTargetStatus{
			Conditions: old.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for syncTarget %s: %w", old.Name, err)
	}

	newData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			UID:             old.UID,
			ResourceVersion: old.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: workloadv1alpha1.SyncTargetStatus{
			Conditions: new.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for syncTarget %s: %w", new.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for syncTarget %s: %w", new.Name, err)
	}
	logger.V(2).Info("patching syncTarget", "patch", string(patchBytes))
	_, uerr := c.syncTargetClient.Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return uerr
}
