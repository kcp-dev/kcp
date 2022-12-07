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

package apireconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	ControllerName = "kcp-virtual-apiexport-api-reconciler"
)

type CreateAPIDefinitionFunc func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string, additionalLabelRequirements labels.Requirements) (apidefinition.APIDefinition, error)

// NewAPIReconciler returns a new controller which reconciles APIResourceImport resources
// and delegates the corresponding SyncTargetAPI management to the given SyncTargetAPIManager.
func NewAPIReconciler(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	createAPIDefinition CreateAPIDefinitionFunc,
	createAPIBindingAPIDefinition func(ctx context.Context, clusterName logicalcluster.Name, apiExportName string) (apidefinition.APIDefinition, error),
) (*APIReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &APIReconciler{
		kcpClusterClient: kcpClusterClient,

		apiResourceSchemaLister:  apiResourceSchemaInformer.Lister(),
		apiResourceSchemaIndexer: apiResourceSchemaInformer.Informer().GetIndexer(),

		apiExportLister:  apiExportInformer.Lister(),
		apiExportIndexer: apiExportInformer.Informer().GetIndexer(),

		queue: queue,

		createAPIDefinition:           createAPIDefinition,
		createAPIBindingAPIDefinition: createAPIBindingAPIDefinition,

		apiSets: map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet{},
	}

	indexers.AddIfNotPresentOrDie(
		apiExportInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity,
		},
	)

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj, logger)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIExport(obj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj, logger)
		},
	})

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(obj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(obj, logger)
		},
	})

	return c, nil
}

// APIReconciler is a controller watching APIExports and APIResourceSchemas, and updates the
// API definitions driving the virtual workspace.
type APIReconciler struct {
	kcpClusterClient kcpclientset.ClusterInterface

	apiResourceSchemaLister  apisv1alpha1listers.APIResourceSchemaClusterLister
	apiResourceSchemaIndexer cache.Indexer

	apiExportLister  apisv1alpha1listers.APIExportClusterLister
	apiExportIndexer cache.Indexer

	queue workqueue.RateLimitingInterface

	createAPIDefinition           CreateAPIDefinitionFunc
	createAPIBindingAPIDefinition func(ctx context.Context, clusterName logicalcluster.Name, apiExportName string) (apidefinition.APIDefinition, error)

	mutex   sync.RWMutex // protects the map, not the values!
	apiSets map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet
}

func (c *APIReconciler) enqueueAPIResourceSchema(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	exports, err := c.apiExportLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	if logObj, ok := obj.(logging.Object); ok {
		logger = logging.WithObject(logger, logObj)
	}

	if len(exports) == 0 {
		logger.V(2).Info("No kubernetes APIExport found for APIResourceSchema")
		return
	}

	for _, export := range exports {
		logger.WithValues("apiexport", export.Name).V(2).Info("Queueing APIExport for APIResourceSchema")
		c.enqueueAPIExport(obj, logger.WithValues("reason", "APIResourceSchema change", "apiResourceSchema", name))
	}
}

func (c *APIReconciler) enqueueAPIExport(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing APIExport")
	c.queue.Add(key)
}

func (c *APIReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *APIReconciler) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("starting controller")
	defer logger.Info("shutting down controller")

	go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())

	// stop all watches if the controller is stopped
	defer func() {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		for _, sets := range c.apiSets {
			for _, v := range sets {
				v.TearDown()
			}
		}
	}()

	<-ctx.Done()
}

func (c *APIReconciler) ShutDown() {
	c.queue.ShutDown()
}

func (c *APIReconciler) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *APIReconciler) process(ctx context.Context, key string) error {
	clusterName, _, apiExportName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	apiDomainKey := dynamiccontext.APIDomainKey(clusterName.String() + "/" + apiExportName)

	logger := klog.FromContext(ctx).WithValues("apiDomainKey", apiDomainKey)

	apiExport, err := c.apiExportLister.Cluster(clusterName).Get(apiExportName)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "error getting APIExport")
		return nil // nothing we can do here
	}

	if apiExport != nil {
		logger = logging.WithObject(logger, apiExport)
	}
	ctx = klog.NewContext(ctx, logger)

	return c.reconcile(ctx, apiExport, apiDomainKey)
}

func (c *APIReconciler) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	apiSet, ok := c.apiSets[key]
	return apiSet, ok, nil
}
