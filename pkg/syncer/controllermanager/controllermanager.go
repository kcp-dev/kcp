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
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	ControllerNamePrefix = "syncer-controller-manager-"
)

// InformerSource is a dynamic source of informers per GVR,
// which notifies when informers are added or removed for some GVR.
// It is implemented by the DynamicSharedInformerFactory (in fact by
// both the scoped or cluster-aware variants).
type InformerSource struct {
	// Subscribe registers for informer change notifications, returning a channel to which change notifications are sent.
	// The id argument is the identifier of the subscriber, since there might be several subscribers subscribing
	// to receive events from this InformerSource.
	Subscribe func(id string) <-chan struct{}

	// Informers returns a map of per-resource-type SharedIndexInformers for all types that are
	// known by this informer source, and that are synced.
	//
	// It also returns the list of informers that are known by this informer source, but sill not synced.
	Informers func() (informers map[schema.GroupVersionResource]cache.SharedIndexInformer, notSynced []schema.GroupVersionResource)
}

// ManagedController defines a controller that should be managed by a ControllerManager,
// to be started when the required GVRs are supported, and stopped when the required GVRs
// are not supported anymore.
type ManagedController struct {
	RequiredGVRs []schema.GroupVersionResource
	Create       CreateControllerFunc
}

type StartControllerFunc func(ctx context.Context)
type CreateControllerFunc func(ctx context.Context) (StartControllerFunc, error)

// NewControllerManager creates a new ControllerManager which will manage (create/start/stop) GVR-specific controllers according to informers
// available in the provided InformerSource.
func NewControllerManager(ctx context.Context, suffix string, informerSource InformerSource, controllers map[string]ManagedController) *ControllerManager {
	controllerManager := ControllerManager{
		name:               ControllerNamePrefix + suffix,
		queue:              workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerNamePrefix+suffix),
		informerSource:     informerSource,
		managedControllers: controllers,
		startedControllers: map[string]context.CancelFunc{},
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

// ControllerManager is a component that manages (create/start/stop) GVR-specific controllers according to available GVRs.
// It reacts to the changes of supported GVRs in a DiscoveringDynamicSharedInformerFactory
// (the GVRs for which an informer has been automatically created, started and synced),
// and starts / stops registered GVRs-specific controllers according to the GVRs they depend on.
//
// For example this allows starting PVC / PV controllers only when PVC / PV resources are exposed by the Syncer and UpSyncer
// virtual workspaces, and Informers for them have been started and synced by the corresponding ddsif.
type ControllerManager struct {
	name               string
	queue              workqueue.RateLimitingInterface
	informerSource     InformerSource
	managedControllers map[string]ManagedController
	startedControllers map[string]context.CancelFunc
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

	c.process(ctx)
	c.queue.Forget(key)
	return true
}

func (c *ControllerManager) process(ctx context.Context) {
	logger := klog.FromContext(ctx)
	controllersToStart := map[string]CreateControllerFunc{}
	syncedInformers, notSynced := c.informerSource.Informers()
controllerLoop:
	for controllerName, managedController := range c.managedControllers {
		requiredGVRs := managedController.RequiredGVRs
		for _, gvr := range requiredGVRs {
			informer := syncedInformers[gvr]
			if informer == nil {
				if shared.ContainsGVR(notSynced, gvr) {
					logger.V(2).Info("waiting for the informer to be synced before starting controller", "gvr", gvr, "controller", controllerName)
					c.queue.AddAfter("resync", time.Second)
					continue controllerLoop
				}
				// The informer doesn't even exist for this GVR.
				// Let's ignore this controller for now: one of the required GVRs has no informer started
				// (because it has not been found on the SyncTarget in the supported resources to sync).
				// If this required GVR is supported later on, the updateControllers() method will be called
				// again after an API change notification comes through the informerSource.
				continue controllerLoop
			}
		}
		controllersToStart[controllerName] = managedController.Create
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

	// Create and start missing controllers that have their required GVRs synced
	for controllerName, create := range controllersToStart {
		if _, ok := c.startedControllers[controllerName]; ok {
			// The controller is already started
			continue
		}

		// Create the controller
		start, err := create(ctx)
		if err != nil {
			logger.Error(err, "failed creating controller", "controller", controllerName)
			continue
		}

		// Start the controller
		controllerContext, cancelFunc := context.WithCancel(ctx)
		go start(controllerContext)
		c.startedControllers[controllerName] = cancelFunc
	}
}
