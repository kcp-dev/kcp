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

package kubequota

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/quota/v1/generic"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/resourcequota"
	"k8s.io/kubernetes/pkg/quota/v1/install"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-kube-quota"
)

// Controller manages a single cluster-aware kubequota controller that handles all workspaces.
type Controller struct {
	cancel           context.CancelFunc
	rq               *resourcequota.Controller
	factory          *informer.DiscoveringDynamicSharedInformerFactory
	ignoredResources map[schema.GroupResource]struct{}
}

// NewController creates a new Controller.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	kubeInformerFactory kcpkubernetesinformers.SharedInformerFactory,
	dynamicDiscoverySharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
	quotaRecalculationPeriod time.Duration,
	fullResyncPeriod time.Duration,
	informersStarted <-chan struct{},
) (*Controller, error) {
	ctx, cancel := context.WithCancel(context.Background())

	resourceQuotaClusterInformer := kubeInformerFactory.Core().V1().ResourceQuotas()

	// TODO(ncdc): find a way to support the default configuration. For
	// now, don't use it, because it is difficult to get support for the
	// special evaluators for pods/services/pvcs. The unscoped factory
	// now exposes a real Lister(), which makes this technically
	// achievable - tracked as a follow-up.
	quotaConfiguration := generic.NewConfiguration(nil, install.DefaultIgnoredResources())
	ignoredResources := quotaConfiguration.IgnoredResources()

	rq, err := resourcequota.NewController(ctx, &resourcequota.ControllerOptions{
		QuotaClient:           newCtxResourceQuotasClient(kubeClusterClient),
		ResourceQuotaInformer: newUnscopedRQInformer(resourceQuotaClusterInformer),
		ResyncPeriod:          controller.StaticResyncPeriodFunc(quotaRecalculationPeriod),
		InformerFactory:       newUnscopedInformerFactory(dynamicDiscoverySharedInformerFactory),
		ReplenishmentResyncPeriod: func() time.Duration {
			return fullResyncPeriod
		},
		// TODO(sttts): this discovery function is wrong. It is some
		// aggregation of all logical clusters, but has non-deterministic
		// behaviour if logical clusters don't agree about REST mappings.
		DiscoveryFunc:        dynamicDiscoverySharedInformerFactory.ServerPreferredResources,
		IgnoredResourcesFunc: quotaConfiguration.IgnoredResources,
		InformersStarted:     informersStarted,
		Registry:             generic.NewRegistry(quotaConfiguration.Evaluators()),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create the resource quota controller: %w", err)
	}

	ignored := map[schema.GroupResource]struct{}{}
	for gr := range ignoredResources {
		ignored[gr] = struct{}{}
	}

	return &Controller{
		cancel:           cancel,
		rq:               rq,
		factory:          dynamicDiscoverySharedInformerFactory,
		ignoredResources: ignored,
	}, nil
}

// Starts starts the single kubequota controller and the on-demand ResyncMonitors loop.
func (c *Controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.cancel()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	initialResources, err := resourcequota.GetQuotableResources(c.factory.ServerPreferredResources)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get initial quotable resources: %w", err))
	}
	for gvr := range initialResources {
		if _, ok := c.ignoredResources[gvr.GroupResource()]; ok {
			delete(initialResources, gvr)
		}
	}

	tracker := &gvrTracker{
		knownGVRs:        initialResources,
		ignoredResources: c.ignoredResources,
		ctx:              ctx,
		logger:           logger,
		rq:               c.rq,
	}

	if err := tracker.rq.ResyncMonitors(ctx, tracker.knownGVRs); err != nil {
		logger.Error(err, "error during initial quota monitor resync")
	}

	c.factory.AddGVRLifecycleHandler(ctx, tracker)

	c.rq.Run(ctx, workers)
}

type gvrTracker struct {
	knownGVRs        map[schema.GroupVersionResource]struct{}
	ignoredResources map[schema.GroupResource]struct{}
	ctx              context.Context //nolint:containedctx
	logger           klog.Logger
	rq               *resourcequota.Controller
}

func (t *gvrTracker) GVRAdded(gvr schema.GroupVersionResource) {
	if _, ok := t.ignoredResources[gvr.GroupResource()]; ok {
		return
	}
	if _, ok := t.knownGVRs[gvr]; ok {
		return
	}
	t.knownGVRs[gvr] = struct{}{}
	if err := t.rq.ResyncMonitors(t.ctx, t.knownGVRs); err != nil {
		t.logger.Error(err, "error during quota monitor resync")
	}
	// Re-enqueue every quota so admission's hasUsageStats check sees a
	// status entry for the new GVR before creates of the new resource
	// arrive.
	t.rq.EnqueueAll(t.ctx)
}

func (t *gvrTracker) GVRRemoved(gvr schema.GroupVersionResource) {
	if _, ok := t.knownGVRs[gvr]; !ok {
		return
	}
	delete(t.knownGVRs, gvr)
	if err := t.rq.ResyncMonitors(t.ctx, t.knownGVRs); err != nil {
		t.logger.Error(err, "error during quota monitor resync")
	}
	t.rq.EnqueueAll(t.ctx)
}
