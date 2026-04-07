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

package apireconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

const (
	ControllerName = "kcp-virtual-replication-api-reconciler"
)

type CreateAPIDefinitionFunc func(apiResourceSchema *apisv1alpha1.APIResourceSchema, cachedResource *cachev1alpha1.CachedResource, export *apisv1alpha2.APIExport) (apidefinition.APIDefinition, error)

// NewAPIReconciler returns a new controller which reconciles APIExport resources,
// keeping the APIDefinition for it up-to-date.
func NewAPIReconciler(
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
	createAPIDefinition CreateAPIDefinitionFunc,
) (*APIReconciler, error) {
	c := &APIReconciler{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),

		createAPIDefinition: createAPIDefinition,

		apiSets: make(map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet),

		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
				apisv1alpha2.Resource("apiexports"),
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				path,
				name,
			)
		},
		getAPIResourceSchema: informer.NewScopedGetterWithFallback(
			localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
			globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
		),
		getCachedResourceEndpointSlice: informer.NewScopedGetterWithFallback(
			localKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Lister(),
			globalKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Lister(),
		),
		getCachedResourceByPath: func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error) {
			// Pull only from the global informer! We need CachedResources to come from the cache,
			// so that they have the shard annotation present! See ../builder/wrap.go.
			return indexers.ByPathAndName[*cachev1alpha1.CachedResource](
				cachev1alpha1.Resource("cachedresources"),
				globalKcpInformers.Cache().V1alpha1().CachedResources().Informer().GetIndexer(),
				path,
				name,
			)
		},
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = globalKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger)
		},
	})

	return c, nil
}

// APIReconciler is a controller watching APIExports and APIResourceSchemas, and updates the
// API definitions driving the virtual workspace.
type APIReconciler struct {
	queue workqueue.TypedRateLimitingInterface[string]

	createAPIDefinition CreateAPIDefinitionFunc

	mutex   sync.RWMutex // protects the map, not the values!
	apiSets map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet

	getAPIExportByPath             func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchema           func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCachedResourceByPath        func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error)
	getCachedResourceEndpointSlice func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
}

func (c *APIReconciler) enqueueCachedResourceEndpointSlice(endpointSlice *cachev1alpha1.CachedResourceEndpointSlice, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(endpointSlice)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger = logging.WithObject(logger, endpointSlice)

	logging.WithQueueKey(logger, key).V(4).Info("queueing CachedResourceEndpointSlice")
	c.queue.Add(key)
}

func (c *APIReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *APIReconciler) Start(ctx context.Context) {
	defer utilruntime.HandleCrash()
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
		for _, set := range c.apiSets {
			for _, def := range set {
				def.TearDown()
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *APIReconciler) process(ctx context.Context, key string) error {
	clusterName, _, endpointSliceName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}
	apiDomainKey := dynamiccontext.APIDomainKey(clusterName.String() + "/" + endpointSliceName)

	logger := klog.FromContext(ctx).WithValues("apiDomainKey", apiDomainKey)

	endpointSlice, err := c.getCachedResourceEndpointSlice(clusterName, endpointSliceName)
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "error getting CachedResourceEndpointSlice")
		return nil // nothing we can do here
	}

	if endpointSlice != nil {
		logger = logging.WithObject(logger, endpointSlice)
	}
	ctx = klog.NewContext(ctx, logger)

	return c.reconcile(ctx, endpointSlice, apiDomainKey)
}

func (c *APIReconciler) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	apiSet, ok := c.apiSets[key]
	return apiSet, ok, nil
}
