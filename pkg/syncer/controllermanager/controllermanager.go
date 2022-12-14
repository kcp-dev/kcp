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

package controllermanager

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerNamePrefix = "syncer-controller-manager-"
)

type InformerSource struct {
	Subscribe func(id string) <-chan struct{}
	Informer  func(gvr schema.GroupVersionResource) (informer cache.SharedIndexInformer, known, synced bool)
}

type Controller interface {
	Start(ctx context.Context, numThreads int)
}

type ControllerDefintion struct {
	RequiredGVRs []schema.GroupVersionResource
	NumThreads   int
	Create       func(syncedInformers map[schema.GroupVersionResource]cache.SharedIndexInformer) (Controller, error)
}

func NewControllerManager(ctx context.Context, suffix string, informerSource InformerSource, controllers map[string]ControllerDefintion) *ControllerManager {
	controllerManager := ControllerManager{
		name:                  ControllerNamePrefix + suffix,
		queue:                 workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerNamePrefix+suffix),
		informerSource:        informerSource,
		controllerDefinitions: controllers,
		startedControllers:    map[string]context.CancelFunc{},
	}

	apisChanged := informerSource.Subscribe(controllerManager.name)

	logger := klog.FromContext(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-apisChanged:
				logger.V(4).Info("got API change notification")
				controllerManager.queue.Add("resync") // this queue only ever has one key in it, as long as it's constant we are OK
			}
		}
	}()

	return &controllerManager
}

type ControllerManager struct {
	name                  string
	queue                 workqueue.RateLimitingInterface
	informerSource        InformerSource
	controllerDefinitions map[string]ControllerDefintion
	startedControllers    map[string]context.CancelFunc
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *ControllerManager) Start(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), c.name)
	logger.Info("Starting controller manager")
	defer logger.Info("Shutting down controller manager")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	<-ctx.Done()
}

func (c *ControllerManager) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *ControllerManager) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	c.UpdateControllers(ctx)
	c.queue.Forget(key)
	return true
}

func (c *ControllerManager) UpdateControllers(ctx context.Context) {
	logger := klog.FromContext(ctx)
	controllersToStart := map[string]map[schema.GroupVersionResource]cache.SharedIndexInformer{}
controllerLoop:
	for controllerName, controllerDefinition := range c.controllerDefinitions {
		requiredGVRs := controllerDefinition.RequiredGVRs
		informers := make(map[schema.GroupVersionResource]cache.SharedIndexInformer, len(requiredGVRs))
		for _, gvr := range requiredGVRs {
			if informer, known, synced := c.informerSource.Informer(gvr); !known {
				continue controllerLoop
			} else if !synced {
				logger.V(2).Info("waiting for the informer to be synced before starting controller", "gvr", gvr, "controller", controllerName)
				c.queue.AddAfter("resync", time.Second)
				continue controllerLoop
			} else {
				informers[gvr] = informer
			}
		}
		controllersToStart[controllerName] = informers
	}

	// Create and start missing controllers that have their required GVRs synced
	newlyStartedControllers := map[string]context.CancelFunc{}
	for controllerName, informers := range controllersToStart {
		if _, ok := c.startedControllers[controllerName]; ok {
			// The controller is already started
			continue
		}
		controllerDefinition, ok := c.controllerDefinitions[controllerName]
		if !ok {
			logger.V(2).Info("cannot find controller definition", "controller", controllerName)
			continue
		}

		// Create the controller
		controller, err := controllerDefinition.Create(informers)
		if err != nil {
			logger.Error(err, "error creating controller", "controller", controllerName)
			continue
		}

		for _, informer := range informers {
			if err := informer.GetStore().Resync(); err != nil {
				logger.Error(err, "error resyncing informer controller", "controller", controllerName)
				continue
			}
		}

		// Start the controller
		controllerContext, cancelFunc := context.WithCancel(ctx)
		go controller.Start(controllerContext, controllerDefinition.NumThreads)
		newlyStartedControllers[c.name] = cancelFunc
	}

	// Remove obsolete controllers that don't have their required GVRs anymore
	for controllerName, cancelFunc := range c.startedControllers {
		if _, ok := controllersToStart[controllerName]; ok {
			// The controller is still expected => don't remove it
			continue
		}
		// The controller should not be running
		// Stop it and remove it from the list of started controllers
		cancelFunc()
		delete(c.startedControllers, controllerName)
	}

	// Add missing controllers that were created and started above
	for controllerName, cancelFunc := range newlyStartedControllers {
		c.startedControllers[controllerName] = cancelFunc
	}
}
