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

// Start starts the single GC and the upstream Sync loop.
func (c *Controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.cancel()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	const syncPeriod = 30 * time.Second

	// Run gc.Sync alongside gc.Run, same as upstream kube-controller-manager.
	go c.gc.Sync(ctx, c.factory, syncPeriod)
	c.gc.Run(ctx, workers, syncPeriod)
}
