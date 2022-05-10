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

package syncer

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Controller struct {
	controllerName string
	queue          workqueue.RateLimitingInterface

	process func(ctx context.Context, gvr schema.GroupVersionResource, key string) error
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(controllerName string, process func(ctx context.Context, gvr schema.GroupVersionResource, key string) error) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	return &Controller{
		controllerName: controllerName,
		queue:          queue,
		process:        process,
	}, nil
}

type queueKey struct {
	gvr schema.GroupVersionResource
	key string // meta namespace key
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("%s queueing GVR %q %s", c.controllerName, gvr.String(), key)
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

	klog.InfoS("Starting syncer workers", "controller", c.controllerName)
	defer klog.InfoS("Stopping syncer workers", "controller", c.controllerName)
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

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key); err != nil {
		runtime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", c.controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
