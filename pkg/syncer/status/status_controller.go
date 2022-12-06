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

package status

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
)

const (
	controllerName = "kcp-workload-syncer-status"
)

type Controller struct {
	queue workqueue.RateLimitingInterface

	upstreamClient            kcpdynamic.ClusterInterface
	downstreamClient          dynamic.Interface
	downstreamNamespaceLister cache.GenericLister

	syncerInformers           resourcesync.SyncerInformerFactory
	syncTargetName            string
	syncTargetWorkspace       logicalcluster.Path
	syncTargetUID             types.UID
	syncTargetKey             string
	advancedSchedulingEnabled bool
}

func NewStatusSyncer(syncerLogger logr.Logger, syncTargetWorkspace logicalcluster.Path, syncTargetName, syncTargetKey string, advancedSchedulingEnabled bool,
	upstreamClient kcpdynamic.ClusterInterface, downstreamClient dynamic.Interface, downstreamInformers dynamicinformer.DynamicSharedInformerFactory, syncerInformers resourcesync.SyncerInformerFactory, syncTargetUID types.UID) (*Controller, error) {

	c := &Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		upstreamClient:            upstreamClient,
		downstreamClient:          downstreamClient,
		downstreamNamespaceLister: downstreamInformers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Lister(),

		syncerInformers:           syncerInformers,
		syncTargetName:            syncTargetName,
		syncTargetWorkspace:       syncTargetWorkspace,
		syncTargetUID:             syncTargetUID,
		syncTargetKey:             syncTargetKey,
		advancedSchedulingEnabled: advancedSchedulingEnabled,
	}

	logger := logging.WithReconciler(syncerLogger, controllerName)

	syncerInformers.AddDownstreamEventHandler(
		func(gvr schema.GroupVersionResource) cache.ResourceEventHandler {
			logger.V(2).Info("Set up downstream informer", "gvr", gvr.String())
			return cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					c.AddToQueue(gvr, obj, logger)
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					oldUnstrob := oldObj.(*unstructured.Unstructured)
					newUnstrob := newObj.(*unstructured.Unstructured)

					if !deepEqualFinalizersAndStatus(oldUnstrob, newUnstrob) {
						c.AddToQueue(gvr, newUnstrob, logger)
					}
				},
				DeleteFunc: func(obj interface{}) {
					c.AddToQueue(gvr, obj, logger)
				},
			}
		})

	return c, nil
}

type queueKey struct {
	gvr schema.GroupVersionResource
	key string // meta namespace key
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing GVR", "gvr", gvr.String())
	c.queue.Add(
		queueKey{
			gvr: gvr,
			key: key,
		},
	)
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting syncer workers")
	defer logger.Info("Stopping syncer workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	qk := key.(queueKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk.key).WithValues("gvr", qk.gvr.String())
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key); err != nil {
		runtime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
