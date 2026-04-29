/*
Copyright 2022 The kcp Authors.

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

package garbagecollector

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/garbagecollector"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadataclient "github.com/kcp-dev/client-go/metadata"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/projection"
)

const (
	ControllerName = "kcp-garbage-collector"
)

// Controller manages a single cluster-aware garbage collector that handles all workspaces.
type Controller struct {
	cancel           context.CancelFunc
	gc               *garbagecollector.GarbageCollector
	factory          *informer.DiscoveringDynamicSharedInformerFactory
	ignoredResources map[schema.GroupResource]struct{}
}

// NewController creates a new Controller.
func NewController(
	kubeClusterClient *kcpkubernetesclientset.ClusterClientset,
	metadataClusterClient kcpmetadataclient.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
	informersStarted <-chan struct{},
) (*Controller, error) {
	ctx, cancel := context.WithCancel(context.Background())

	ignoredResources := defaultIgnoredResources()

	gc, err := garbagecollector.NewGarbageCollector(
		ctx,
		kubeClusterClient,
		newCtxMetadataClient(metadataClusterClient),
		dynamicDiscoverySharedInformerFactory.RESTMapper(),
		ignoredResources,
		newUnscopedInformerFactory(dynamicDiscoverySharedInformerFactory),
		informersStarted,
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create the garbage collector: %w", err)
	}

	c := new(Controller)
	c.cancel = cancel
	c.ignoredResources = ignoredResources
	c.gc = gc
	c.factory = dynamicDiscoverySharedInformerFactory
	return c, nil
}

func defaultIgnoredResources() (ret map[schema.GroupResource]struct{}) {
	ret = make(map[schema.GroupResource]struct{})
	// Add default ignored resources
	for gr := range garbagecollector.DefaultIgnoredResources() {
		ret[gr] = struct{}{}
	}
	// Add projected API resources
	for gvr := range projection.ProjectedAPIs() {
		ret[gvr.GroupResource()] = struct{}{}
	}
	return ret
}

// Start starts the single GC and the on-demand Sync loop.
func (c *Controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.cancel()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	initialResources, err := garbagecollector.GetDeletableResources(logger, c.factory)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get initial deletable resources: %w", err))
	}
	for gvr := range initialResources {
		if _, ok := c.ignoredResources[gvr.GroupResource()]; ok {
			delete(initialResources, gvr)
		}
	}

	tracker := &gvrTracker{
		knownGVRs:        initialResources,
		ignoredResources: c.ignoredResources,
		logger:           logger,
		gc:               c.gc,
	}

	if err := tracker.gc.ResyncMonitors(logger, tracker.knownGVRs); err != nil {
		logger.Error(err, "error during resync")
	}

	c.factory.AddGVRLifecycleHandler(ctx, tracker)

	// 30s is the initial sync timeout that upstream also uses.
	// The period isn't that important - even if the graph builder isn't
	// sycned within this period it lust logs an informational message.
	c.gc.Run(ctx, workers, 30*time.Second)
}

type gvrTracker struct {
	knownGVRs        map[schema.GroupVersionResource]struct{}
	ignoredResources map[schema.GroupResource]struct{}
	logger           klog.Logger
	gc               *garbagecollector.GarbageCollector
}

func (tracker *gvrTracker) GVRAdded(gvr schema.GroupVersionResource) {
	if _, ok := tracker.ignoredResources[gvr.GroupResource()]; ok {
		return
	}
	if _, ok := tracker.knownGVRs[gvr]; ok {
		// guard to prevent "empty" updates when the GVR is already known
		return
	}
	tracker.knownGVRs[gvr] = struct{}{}
	if err := tracker.gc.ResyncMonitors(tracker.logger, tracker.knownGVRs); err != nil {
		tracker.logger.Error(err, "error during resync")
	}
}

func (tracker *gvrTracker) GVRRemoved(gvr schema.GroupVersionResource) {
	if _, ok := tracker.knownGVRs[gvr]; !ok {
		// guard to prevent "empty" updates when the GVR is already gone
		return
	}
	delete(tracker.knownGVRs, gvr)
	if err := tracker.gc.ResyncMonitors(tracker.logger, tracker.knownGVRs); err != nil {
		tracker.logger.Error(err, "error during resync")
	}
}
