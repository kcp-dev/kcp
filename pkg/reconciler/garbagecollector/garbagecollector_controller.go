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
	cancel  context.CancelFunc
	gc      *garbagecollector.GarbageCollector
	factory *informer.DiscoveringDynamicSharedInformerFactory
}

// NewController creates a new Controller.
func NewController(
	kubeClusterClient *kcpkubernetesclientset.ClusterClientset,
	metadataClusterClient kcpmetadataclient.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
	informersStarted <-chan struct{},
) (*Controller, error) {
	ctx, cancel := context.WithCancel(context.Background())

	gc, err := garbagecollector.NewGarbageCollector(
		ctx,
		kubeClusterClient,
		newCtxMetadataClient(metadataClusterClient),
		dynamicDiscoverySharedInformerFactory.RESTMapper(),
		defaultIgnoredResources(),
		newUnscopedInformerFactory(dynamicDiscoverySharedInformerFactory),
		informersStarted,
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create the garbage collector: %w", err)
	}

	c := new(Controller)
	c.cancel = cancel
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

	go c.runOnDemandSync(ctx)

	// 30s is the initial sync timeout that upstream also uses.
	// The period isn't that important - even if the graph builder isn't
	// sycned within this period it lust logs an informational message.
	c.gc.Run(ctx, workers, 30*time.Second)
}

// runOnDemandSync triggers the garbage collector monitor sync whenever the DDSIF registers a change.
func (c *Controller) runOnDemandSync(ctx context.Context) {
	logger := klog.FromContext(ctx)
	apisChanged := c.factory.Subscribe(ControllerName)
	for {
		select {
		case <-ctx.Done():
			return
		case <-apisChanged:
			logger.V(4).Info("got API change notification, triggering garbage collector sync")
			c.syncOnce(ctx)
		}
	}
}

// syncOnce runs exactly one iteration of gc.Sync's body.
func (c *Controller) syncOnce(ctx context.Context) {
	oneShotCtx := newNonPropagatingCtx(ctx)
	defer oneShotCtx.cancel()

	// Start a goroutine running the sync with the non-propagating context.
	// Sync uses wait.UntilWithContext. By cancelling the context after
	// 2s we are stopping the wait.UntilX from performing more
	// iterations. Since the context does not propagate cancellation we
	// are also not stopping other context-based tasks from stopping.

	// foreverSyncPeriod is passed to .Sync which then passes that as
	// the wait period to wait.UntilWithContext. Setting a high value
	// ensures that even if the body finishes very quickly (e.g. within
	// the 2s timeout to cancel the non propagating context) it doesn't
	// run a second iteration.
	foreverSyncPeriod := 1 * time.Hour

	done := make(chan struct{})
	go func() {
		// .Sync will return after the one iteration, close the done
		// channel which the main function is waiting on to not exit
		// before the one sync iteration is done.
		c.gc.Sync(oneShotCtx, c.factory, foreverSyncPeriod)
		close(done)
	}()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
	}

	oneShotCtx.cancel()
	<-done
}
