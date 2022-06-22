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

package kubequota

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/quota/v1/generic"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/metrics/prometheus/ratelimiter"
	"k8s.io/controller-manager/pkg/informerfactory"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/resourcequota"
	"k8s.io/kubernetes/pkg/quota/v1/install"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
)

const (
	controllerName = "kcp-kube-quota"
)

// Controller manages per-workspace resource quota controllers.
type Controller struct {
	queue workqueue.RateLimitingInterface

	resourceQuotaInformer   coreinformers.ResourceQuotaInformer
	informerFactoryForQuota informerfactory.InformerFactory
	kubeClusterClient       kubernetes.ClusterInterface
	informersStarted        <-chan struct{}

	// TODO(ncdc): see if these are interpreted correctly from upstream.
	// quotaRecalculationPeriod controls how often a full quota recalculation is performed
	quotaRecalculationPeriod time.Duration
	// fullResyncPeriod controls how often the dynamic informers do a full resync
	fullResyncPeriod time.Duration

	workersPerLogicalCluster int

	// lock guards the fields in this group
	lock                       sync.RWMutex
	cancelFuncs                map[logicalcluster.Name]func()
	resourceQuotaEventHandlers map[logicalcluster.Name]cache.ResourceEventHandler

	delegatingEventHandler *delegatingEventHandler

	discoveryStopsLock sync.Mutex
	discoveryStops     map[logicalcluster.Name]chan struct{}

	// For better testability
	getClusterWorkspace func(key string) (*tenancyv1alpha1.ClusterWorkspace, error)
}

// NewController creates a new Controller.
func NewController(
	clusterWorkspacesInformer tenancyinformers.ClusterWorkspaceInformer,
	kubeClusterClient kubernetes.ClusterInterface,
	kubeInformerFactory informers.SharedInformerFactory,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	quotaRecalculationPeriod time.Duration,
	fullResyncPeriod time.Duration,
	workersPerLogicalCluster int,
	informersStarted <-chan struct{},
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		resourceQuotaInformer:   kubeInformerFactory.Core().V1().ResourceQuotas(),
		informerFactoryForQuota: NewInformerFactory(kubeInformerFactory, dynamicDiscoverySharedInformerFactory),
		kubeClusterClient:       kubeClusterClient,
		informersStarted:        informersStarted,

		quotaRecalculationPeriod: quotaRecalculationPeriod,
		fullResyncPeriod:         fullResyncPeriod,

		workersPerLogicalCluster: workersPerLogicalCluster,

		cancelFuncs:                map[logicalcluster.Name]func(){},
		resourceQuotaEventHandlers: map[logicalcluster.Name]cache.ResourceEventHandler{},
		delegatingEventHandler: &delegatingEventHandler{
			eventHandlers: map[schema.GroupResource]map[logicalcluster.Name]cache.ResourceEventHandler{},
		},

		discoveryStops: map[logicalcluster.Name]chan struct{}{},

		getClusterWorkspace: func(key string) (*tenancyv1alpha1.ClusterWorkspace, error) {
			return clusterWorkspacesInformer.Lister().Get(key)
		},
	}

	clusterWorkspacesInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueue(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				c.enqueue(oldObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueue(obj)
			},
		},
	)

	// This sets up a single global event handler on ResourceQuotas. When a resource quota controller is created for
	// a logical cluster, it registers its own event handler, which is stored in Controller.resourceQuotaEventHandlers.
	// When an event comes in, this handler looks up the actual handler for the object's logical cluster and invokes it.
	c.resourceQuotaInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				h := c.getResourceQuotaEventHandler(obj)
				if h == nil {
					return
				}
				h.OnAdd(obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				h := c.getResourceQuotaEventHandler(oldObj)
				if h == nil {
					return
				}
				h.OnUpdate(oldObj, newObj)
			},
			DeleteFunc: func(obj interface{}) {
				h := c.getResourceQuotaEventHandler(obj)
				if h == nil {
					return
				}
				h.OnDelete(obj)
			},
		},
		quotaRecalculationPeriod,
	)

	return c, nil
}

func clusterNameForObj(obj interface{}) logicalcluster.Name {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return logicalcluster.Name{}
	}

	_, clusterAndName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return logicalcluster.Name{}
	}

	cluster, _ := clusters.SplitClusterAwareKey(clusterAndName)
	return cluster
}

// getResourceQuotaEventHandler returns the ResourceQuota cache.ResourceEventHandler for obj's logical cluster.
func (c *Controller) getResourceQuotaEventHandler(obj interface{}) cache.ResourceEventHandler {
	cluster := clusterNameForObj(obj)
	if cluster.Empty() {
		return nil
	}

	c.lock.RLock()
	h := c.resourceQuotaEventHandlers[cluster]
	c.lock.RUnlock()

	return h
}

// registerResourceQuotaEventHandlerLocked registers a cache.ResourceEventHandler for ResourceQuotas for the specific
// logical cluster specified by clusterName. Controller.lock must be held before calling this function.
func (c *Controller) registerResourceQuotaEventHandlerLocked(clusterName logicalcluster.Name, h cache.ResourceEventHandler) {
	c.resourceQuotaEventHandlers[clusterName] = h
}

// enqueue adds the key for a ClusterWorkspace to the queue.
func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	klog.V(2).InfoS("Enqueuing ClusterWorkspace", "key", key)
	c.queue.Add(key)
}

// Start starts the controller.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting down %s controller", controllerName)

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

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// process processes a single key from the queue.
func (c *Controller) process(ctx context.Context, key string) error {
	// e.g. root:org<separator>ws
	parent, name := clusters.SplitClusterAwareKey(key)

	// turn it into root:org:ws
	clusterName := parent.Join(name)

	_, err := c.getClusterWorkspace(key)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.V(2).InfoS("ClusterWorkspace not found - stopping quota controller for it (if needed)", "clusterName", clusterName)

			c.lock.Lock()
			cancel, ok := c.cancelFuncs[clusterName]
			if ok {
				cancel()
				delete(c.cancelFuncs, clusterName)
			}
			c.lock.Unlock()

			return nil
		}

		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	_, found := c.cancelFuncs[clusterName]
	if found {
		klog.V(4).InfoS("Quota controller already exists", "clusterName", clusterName)
		return nil
	}

	klog.V(2).InfoS("Starting quota controller", "clusterName", clusterName)

	ctx, cancel := context.WithCancel(ctx)
	c.cancelFuncs[clusterName] = cancel

	if err := c.startQuotaForClusterWorkspace(ctx, clusterName); err != nil {
		return fmt.Errorf("error starting quota controller for cluster %q: %w", clusterName, err)
	}

	return nil
}

func (c *Controller) startQuotaForClusterWorkspace(ctx context.Context, clusterName logicalcluster.Name) error {
	resourceQuotaControllerClient := c.kubeClusterClient.Cluster(clusterName)
	resourceQuotaControllerDiscoveryClient := resourceQuotaControllerClient.Discovery()

	discoveryFunc := resourceQuotaControllerDiscoveryClient.ServerPreferredNamespacedResources

	scopedInformerFactory := &scopedGenericSharedInformerFactory{
		delegate:               c.informerFactoryForQuota,
		clusterName:            clusterName,
		delegatingEventHandler: c.delegatingEventHandler,
		informers:              map[schema.GroupVersionResource]*scopedGenericInformer{},
	}

	// TODO(ncdc): find a way to support the default configuration. For now, don't use it, because it is difficult
	// to get support for the special evaluators for pods/services/pvcs.
	// listerFuncForResource := generic.ListerFuncForResourceFunc(scopedInformerFactory.ForResource)
	// quotaConfiguration := install.NewQuotaConfigurationForControllers(listerFuncForResource)
	quotaConfiguration := generic.NewConfiguration(nil, install.DefaultIgnoredResources())

	resourceQuotaControllerOptions := &resourcequota.ControllerOptions{
		QuotaClient: resourceQuotaControllerClient.CoreV1(),
		ResourceQuotaInformer: &SingleClusterResourceQuotaInformer{
			clusterName:     clusterName,
			delegate:        c.resourceQuotaInformer,
			registerHandler: c.registerResourceQuotaEventHandlerLocked,
		},
		ResyncPeriod:    controller.StaticResyncPeriodFunc(c.quotaRecalculationPeriod),
		InformerFactory: scopedInformerFactory,
		ReplenishmentResyncPeriod: func() time.Duration {
			return c.fullResyncPeriod
		},
		DiscoveryFunc:        discoveryFunc,
		IgnoredResourcesFunc: quotaConfiguration.IgnoredResources,
		InformersStarted:     c.informersStarted,
		Registry:             generic.NewRegistry(quotaConfiguration.Evaluators()),
	}
	if resourceQuotaControllerClient.CoreV1().RESTClient().GetRateLimiter() != nil {
		if err := ratelimiter.RegisterMetricAndTrackRateLimiterUsage(clusterName.String()+"-resource_quota_controller", resourceQuotaControllerClient.CoreV1().RESTClient().GetRateLimiter()); err != nil {
			return err
		}
	}

	resourceQuotaController, err := resourcequota.NewController(resourceQuotaControllerOptions)
	if err != nil {
		return err
	}

	go resourceQuotaController.Run(ctx, c.workersPerLogicalCluster)

	// Here we diverge from what upstream does. Upstream starts a goroutine that retrieves discovery every 30 seconds,
	// starting/stopping dynamic informers as needed based on the updated discovery data. We know that discovery changes
	// when APIBindings and/or CRDs in the logical cluster change, so we "kick" the discovery goroutine when these
	// events occur.

	// This function is called whenever we get a watch event for APIBindings or CRDs
	resetDiscovery := func() {
		c.discoveryStopsLock.Lock()
		defer c.discoveryStopsLock.Unlock()

		stop, ok := c.discoveryStops[clusterName]
		if ok {
			klog.V(2).InfoS("Stopping quota discovery Sync goroutine", "clusterName", clusterName)
			close(stop)
		}

		stop = make(chan struct{})
		c.discoveryStops[clusterName] = stop

		// Cache discovery - this won't potentially change unless/until another APIBinding/CRD watch event comes in
		apis, err := discoveryFunc()

		klog.V(2).InfoS("Starting quota discovery Sync goroutine", "clusterName", clusterName)
		go resourceQuotaController.Sync(
			// Return the cached discovery data + error
			func() ([]*metav1.APIResourceList, error) {
				return apis, err
			},
			// This value doesn't really have any meaning, as we're only ever returning the cached discovery data.
			24*time.Hour,
			stop,
		)
	}

	// Shared event handler
	rediscover := cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ interface{}) {
			resetDiscovery()
		},
		UpdateFunc: func(_, _ interface{}) {
			resetDiscovery()
		},
		DeleteFunc: func(_ interface{}) {
			resetDiscovery()
		},
	}

	// Add the handler for CRDs
	crdGVR := apiextensionsv1.SchemeGroupVersion.WithResource("customresourcedefinitions")
	crdInformer, err := scopedInformerFactory.ForResource(crdGVR)
	if err != nil {
		return err
	}
	crdInformer.Informer().AddEventHandler(rediscover)

	// Add the handler for APIBindings
	apibindingsGVR := apisv1alpha1.SchemeGroupVersion.WithResource("apibindings")
	apiBindingsInformer, err := scopedInformerFactory.ForResource(apibindingsGVR)
	if err != nil {
		return err
	}
	apiBindingsInformer.Informer().AddEventHandler(rediscover)

	return nil
}
