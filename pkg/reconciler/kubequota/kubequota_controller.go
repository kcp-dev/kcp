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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/quota/v1/generic"
	kubernetesinformers "k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/component-base/metrics/prometheus/ratelimiter"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller"
	"k8s.io/kubernetes/pkg/controller/resourcequota"
	"k8s.io/kubernetes/pkg/quota/v1/install"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	controllerName = "kcp-kube-quota"
)

// Controller manages per-workspace resource quota controllers.
type Controller struct {
	queue workqueue.RateLimitingInterface

	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory
	kubeClusterClient                     kubernetesclient.ClusterInterface
	informersStarted                      <-chan struct{}

	// quotaRecalculationPeriod controls how often a full quota recalculation is performed
	quotaRecalculationPeriod time.Duration
	// fullResyncPeriod controls how often the dynamic informers do a full resync
	fullResyncPeriod time.Duration

	workersPerLogicalCluster int

	// lock guards the fields in this group
	lock        sync.RWMutex
	cancelFuncs map[logicalcluster.Name]func()

	scopingResourceQuotaInformer        *ScopingResourceQuotaInformer
	scopingGenericSharedInformerFactory *scopingGenericSharedInformerFactory

	crdsSynced cache.InformerSynced

	// For better testability
	getClusterWorkspace func(key string) (*tenancyv1alpha1.ClusterWorkspace, error)
	listCRDs            func() ([]*apiextensionsv1.CustomResourceDefinition, error)
}

// NewController creates a new Controller.
func NewController(
	clusterWorkspacesInformer tenancyinformers.ClusterWorkspaceInformer,
	kubeClusterClient kubernetesclient.ClusterInterface,
	kubeInformerFactory kubernetesinformers.SharedInformerFactory,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	crdInformer apiextensionsinformers.CustomResourceDefinitionInformer,
	quotaRecalculationPeriod time.Duration,
	fullResyncPeriod time.Duration,
	workersPerLogicalCluster int,
	informersStarted <-chan struct{},
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		dynamicDiscoverySharedInformerFactory: dynamicDiscoverySharedInformerFactory,
		kubeClusterClient:                     kubeClusterClient,
		informersStarted:                      informersStarted,

		quotaRecalculationPeriod: quotaRecalculationPeriod,
		fullResyncPeriod:         fullResyncPeriod,

		workersPerLogicalCluster: workersPerLogicalCluster,

		cancelFuncs: map[logicalcluster.Name]func(){},

		scopingGenericSharedInformerFactory: newScopingGenericSharedInformerFactory(dynamicDiscoverySharedInformerFactory),
		scopingResourceQuotaInformer:        NewScopingResourceQuotaInformer(kubeInformerFactory.Core().V1().ResourceQuotas()),

		crdsSynced: crdInformer.Informer().HasSynced,

		getClusterWorkspace: func(key string) (*tenancyv1alpha1.ClusterWorkspace, error) {
			return clusterWorkspacesInformer.Lister().Get(key)
		},

		listCRDs: func() ([]*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().List(labels.Everything())
		},
	}

	clusterWorkspacesInformer.Informer().AddEventHandler(
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

// enqueue adds the key for a ClusterWorkspace to the queue.
func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logger.V(2).Info("queueing ClusterWorkspace")
	c.queue.Add(key)
}

// Start starts the controller.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	if !cache.WaitForCacheSync(ctx.Done(), c.crdsSynced) {
		logger.Error(errors.New("CRD informer never synced"), "error waiting for informers to sync")
		return
	}

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
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// process processes a single key from the queue.
func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	// e.g. root:org<separator>ws
	parent, name := clusters.SplitClusterAwareKey(key)

	// turn it into root:org:ws
	clusterName := parent.Join(name)
	logger = logger.WithValues("logicalCluster", clusterName.String())

	ws, err := c.getClusterWorkspace(key)
	if err != nil {
		if kerrors.IsNotFound(err) {
			logger.V(2).Info("ClusterWorkspace not found - stopping quota controller for it (if needed)")

			c.lock.Lock()
			cancel, ok := c.cancelFuncs[clusterName]
			if ok {
				cancel()
				delete(c.cancelFuncs, clusterName)
			}
			c.lock.Unlock()

			c.dynamicDiscoverySharedInformerFactory.Unsubscribe("quota-" + clusterName.String())

			return nil
		}

		return err
	}
	logger = logging.WithObject(logger, ws)

	c.lock.Lock()
	defer c.lock.Unlock()

	_, found := c.cancelFuncs[clusterName]
	if found {
		logger.V(4).Info("quota controller already exists")
		return nil
	}

	logger.V(2).Info("starting quota controller")

	ctx, cancel := context.WithCancel(ctx)
	ctx = klog.NewContext(ctx, logger)
	c.cancelFuncs[clusterName] = cancel

	if err := c.startQuotaForClusterWorkspace(ctx, clusterName); err != nil {
		cancel()
		return fmt.Errorf("error starting quota controller for cluster %q: %w", clusterName, err)
	}

	return nil
}

func (c *Controller) startQuotaForClusterWorkspace(ctx context.Context, clusterName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	resourceQuotaControllerClient := c.kubeClusterClient.Cluster(clusterName)

	// TODO(ncdc): find a way to support the default configuration. For now, don't use it, because it is difficult
	// to get support for the special evaluators for pods/services/pvcs.
	// listerFuncForResource := generic.ListerFuncForResourceFunc(scopedInformerFactory.ForResource)
	// quotaConfiguration := install.NewQuotaConfigurationForControllers(listerFuncForResource)
	quotaConfiguration := generic.NewConfiguration(nil, install.DefaultIgnoredResources())

	resourceQuotaControllerOptions := &resourcequota.ControllerOptions{
		QuotaClient:           resourceQuotaControllerClient.CoreV1(),
		ResourceQuotaInformer: c.scopingResourceQuotaInformer.ForCluster(clusterName),
		ResyncPeriod:          controller.StaticResyncPeriodFunc(c.quotaRecalculationPeriod),
		InformerFactory:       c.scopingGenericSharedInformerFactory.ForCluster(clusterName),
		ReplenishmentResyncPeriod: func() time.Duration {
			return c.fullResyncPeriod
		},
		DiscoveryFunc:        c.dynamicDiscoverySharedInformerFactory.DiscoveryData,
		IgnoredResourcesFunc: quotaConfiguration.IgnoredResources,
		InformersStarted:     c.informersStarted,
		Registry:             generic.NewRegistry(quotaConfiguration.Evaluators()),
		ClusterName:          clusterName,
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

	// Here we diverge from what upstream does. Upstream starts a goroutine that retrieves discovery every 30 seconds,
	// starting/stopping dynamic informers as needed based on the updated discovery data. We know that kcp contains
	// the combination of built-in types plus CRDs. We use that information to drive what quota evaluates.

	apisChanged := c.dynamicDiscoverySharedInformerFactory.Subscribe("quota-" + clusterName.String())

	go func() {
		var discoveryCancel func()

		for {
			select {
			case <-ctx.Done():
				if discoveryCancel != nil {
					discoveryCancel()
				}

				return
			case <-apisChanged:
				if discoveryCancel != nil {
					discoveryCancel()
				}

				logger.V(4).Info("got API change notification")

				ctx, discoveryCancel = context.WithCancel(ctx)
				resourceQuotaController.UpdateMonitors(ctx, c.dynamicDiscoverySharedInformerFactory.DiscoveryData)
			}
		}
	}()

	go resourceQuotaController.Run(ctx, c.workersPerLogicalCluster)

	return nil
}
