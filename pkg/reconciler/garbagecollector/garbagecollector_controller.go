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

package garbagecollector

import (
	"context"
	"fmt"
	"sync"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadataclient "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/metrics/prometheus/ratelimiter"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/garbagecollector"

	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
	tenancyv1beta1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/projection"
)

const (
	ControllerName = "kcp-garbage-collector"
)

// Controller manages per-workspace garbage collector controllers.
type Controller struct {
	queue workqueue.RateLimitingInterface

	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory
	kubeClusterClient                     kcpkubernetesclient.ClusterInterface
	metadataClient                        kcpmetadataclient.ClusterInterface
	workspaceLister                       tenancyv1beta1listers.WorkspaceClusterLister
	informersStarted                      <-chan struct{}

	workersPerLogicalCluster int

	// lock guards the fields in this group
	lock        sync.RWMutex
	cancelFuncs map[logicalcluster.Name]func()

	ignoredResources map[schema.GroupResource]struct{}
}

// NewController creates a new Controller.
func NewController(
	workspaceInformer tenancyv1beta1informers.WorkspaceClusterInformer,
	kubeClusterClient kcpkubernetesclient.ClusterInterface,
	metadataClient kcpmetadataclient.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	workersPerLogicalCluster int,
	informersStarted <-chan struct{},
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),

		dynamicDiscoverySharedInformerFactory: dynamicDiscoverySharedInformerFactory,
		kubeClusterClient:                     kubeClusterClient,
		metadataClient:                        metadataClient,
		workspaceLister:                       workspaceInformer.Lister(),
		informersStarted:                      informersStarted,

		workersPerLogicalCluster: workersPerLogicalCluster,

		cancelFuncs: map[logicalcluster.Name]func(){},

		ignoredResources: defaultIgnoredResources(),
	}

	workspaceInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.enqueue,
			UpdateFunc: func(oldObj, newObj interface{}) {
				c.enqueue(oldObj)
			},
			DeleteFunc: c.enqueue,
		},
	)

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

// enqueue adds the key for a ClusterWorkspace to the queue.
func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing ClusterWorkspace")
	c.queue.Add(key)
}

// Start starts the controller.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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

// startWorker runs a single worker goroutine.
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

// processNextWorkItem waits for the queue to have a key available and then processes it.
func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	raw, quit := c.queue.Get()
	if quit {
		return false
	}
	key := raw.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// process processes a single key from the queue.
func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	// turn it into root:org:ws
	clusterName := parent.Join(name)
	logger = logger.WithValues("logicalCluster", clusterName.String())

	ws, err := c.workspaceLister.Cluster(parent).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			logger.V(2).Info("ClusterWorkspace not found - stopping garbage collector controller for it (if needed)")

			c.lock.Lock()
			cancel, ok := c.cancelFuncs[clusterName]
			if ok {
				cancel()
				delete(c.cancelFuncs, clusterName)
			}
			c.lock.Unlock()

			c.dynamicDiscoverySharedInformerFactory.Unsubscribe("gc-" + clusterName.String())

			return nil
		}

		return err
	}
	logger = logging.WithObject(logger, ws)

	c.lock.Lock()
	defer c.lock.Unlock()

	_, found := c.cancelFuncs[clusterName]
	if found {
		logger.V(4).Info("garbage collector controller already exists")
		return nil
	}

	logger.V(2).Info("starting garbage collector controller")

	ctx, cancel := context.WithCancel(ctx)
	ctx = klog.NewContext(ctx, logger)
	c.cancelFuncs[clusterName] = cancel

	if err := c.startGarbageCollectorForClusterWorkspace(ctx, clusterName); err != nil {
		cancel()
		return fmt.Errorf("error starting garbage collector controller for cluster %q: %w", clusterName, err)
	}

	return nil
}

func (c *Controller) startGarbageCollectorForClusterWorkspace(ctx context.Context, clusterName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)

	kubeClient := c.kubeClusterClient.Cluster(clusterName)

	garbageCollector, err := garbagecollector.NewClusterAwareGarbageCollector(
		kubeClient,
		c.metadataClient.Cluster(clusterName),
		c.dynamicDiscoverySharedInformerFactory.RESTMapper(),
		c.ignoredResources,
		c.dynamicDiscoverySharedInformerFactory.Cluster(clusterName),
		c.informersStarted,
		clusterName,
	)
	if err != nil {
		return fmt.Errorf("failed to create the garbage collector: %w", err)
	}

	if kubeClient.CoreV1().RESTClient().GetRateLimiter() != nil {
		if err := ratelimiter.RegisterMetricAndTrackRateLimiterUsage(clusterName.String()+"-garbage_collector_controller", kubeClient.CoreV1().RESTClient().GetRateLimiter()); err != nil {
			return err
		}
	}

	// Here we diverge from what upstream does. Upstream starts a goroutine that retrieves discovery every 30 seconds,
	// starting/stopping dynamic informers as needed based on the updated discovery data. We know that kcp contains
	// the combination of built-in types plus CRDs. We use that information to drive what garbage collector evaluates.
	// TODO: support scoped shared dynamic discovery to avoid emitting global discovery events.

	garbageCollectorController := garbageCollectorController{
		clusterName: clusterName,
		queue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "quota-"+clusterName.String()),
		work: func(ctx context.Context) {
			garbageCollector.ResyncMonitors(ctx, c.dynamicDiscoverySharedInformerFactory)
		},
	}
	go garbageCollectorController.Start(ctx)

	apisChanged := c.dynamicDiscoverySharedInformerFactory.Subscribe("gc-" + clusterName.String())

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-apisChanged:
				logger.V(4).Info("got API change notification")
				garbageCollectorController.queue.Add("resync") // this queue only ever has one key in it, as long as it's constant we are OK
			}
		}
	}()

	// Make sure the GC monitors are synced at least once
	garbageCollector.ResyncMonitors(ctx, c.dynamicDiscoverySharedInformerFactory)

	go garbageCollector.Run(ctx, c.workersPerLogicalCluster)

	return nil
}

type garbageCollectorController struct {
	clusterName    logicalcluster.Name
	queue          workqueue.RateLimitingInterface
	work           func(context.Context)
	previousCancel func()
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *garbageCollectorController) Start(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName+"-"+c.clusterName.String()+"-monitors")
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	<-ctx.Done()
	if c.previousCancel != nil {
		c.previousCancel()
	}
}

func (c *garbageCollectorController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *garbageCollectorController) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	if c.previousCancel != nil {
		c.previousCancel()
	}

	ctx, c.previousCancel = context.WithCancel(ctx)
	c.work(ctx)
	c.queue.Forget(key)
	return true
}
