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

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformer "github.com/kcp-dev/kcp/pkg/client/informers/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/client/informers/workload/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancylistersv1alpha1 "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	ControllerName = "kcp-virtual-syncer-api-reconciler"
	byWorkspace    = ControllerName + "-byWorkspace" // will go away with scoping
)

type CreateAPIDefinitionFunc func(syncTargetWorkspace logicalcluster.Name, syncTargetName string, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string) (apidefinition.APIDefinition, error)

func NewAPIReconciler(
	kcpClusterClient kcpclient.ClusterInterface,
	syncTargetInformer tenancyv1alpha1.SyncTargetInformer,
	apiResourceSchemaInformer apisinformer.APIResourceSchemaInformer,
	apiExportInformer apisinformer.APIExportInformer,
	createAPIDefinition CreateAPIDefinitionFunc,
) (*APIReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &APIReconciler{
		kcpClusterClient: kcpClusterClient,

		syncTargetLister:  syncTargetInformer.Lister(),
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),

		apiResourceSchemaLister:  apiResourceSchemaInformer.Lister(),
		apiResourceSchemaIndexer: apiResourceSchemaInformer.Informer().GetIndexer(),

		apiExportLister:  apiExportInformer.Lister(),
		apiExportIndexer: apiExportInformer.Informer().GetIndexer(),

		queue: queue,

		createAPIDefinition: createAPIDefinition,

		apiSets: map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet{},
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := apiResourceSchemaInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := apiExportInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueSyncTarget(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueSyncTarget(obj)
		},
	})

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(obj)
		},
	})

	apiExportInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err != nil {
				return false
			}
			_, name := clusters.SplitClusterAwareKey(key)
			return name == apiexport.TemporaryComputeServiceExportName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueAPIExport(obj)
			},
			UpdateFunc: func(_, obj interface{}) {
				c.enqueueAPIExport(obj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueAPIExport(obj)
			},
		},
	})

	return c, nil
}

// APIReconciler is a controller watching APIExports, APIResourceSchemas and SyncTargets, and updates the
// API definitions driving the virtual workspace.
type APIReconciler struct {
	kcpClusterClient kcpclient.ClusterInterface

	syncTargetLister  tenancylistersv1alpha1.SyncTargetLister
	syncTargetIndexer cache.Indexer

	apiResourceSchemaLister  apislisters.APIResourceSchemaLister
	apiResourceSchemaIndexer cache.Indexer

	apiExportLister  apislisters.APIExportLister
	apiExportIndexer cache.Indexer

	queue workqueue.RateLimitingInterface

	createAPIDefinition CreateAPIDefinitionFunc

	mutex   sync.RWMutex // protects the map, not the values!
	apiSets map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet
}

func (c *APIReconciler) enqueueSyncTarget(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	clusterName, name := clusters.SplitClusterAwareKey(key)
	exports, err := c.apiExportIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	if len(exports) == 0 {
		klog.V(2).Infof("No kubernetes APIExport found for SyncTarget %s|%s", clusterName, name)
		return
	}

	for _, obj := range exports {
		export := obj.(*apisv1alpha1.APIExport)
		klog.V(2).Infof("Queueing APIExport %s|%s for SyncTarget %s", clusterName, export.Name, name)
		c.enqueueAPIExport(obj)
	}
}

func (c *APIReconciler) enqueueAPIResourceSchema(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	clusterName, name := clusters.SplitClusterAwareKey(key)
	exports, err := c.apiExportIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range exports {
		export := obj.(*apisv1alpha1.APIExport)
		klog.V(2).Infof("Queueing APIExport %s|%s for APIResourceSchema %s", clusterName, export.Name, name)
		c.enqueueAPIExport(obj)
	}
}

func (c *APIReconciler) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *APIReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *APIReconciler) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", ControllerName)
	defer klog.Infof("Shutting down %s controller", ControllerName)

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
	clusterName, apiExportName := clusters.SplitClusterAwareKey(key)
	apiExport, err := c.apiExportLister.Get(key)
	if err != nil && !apierrors.IsNotFound(err) {
		klog.Errorf("failed to get APIExport %s|%s from lister: %v", clusterName, apiExportName, err)
		return nil // nothing we can do here
	}

	cs, err := c.syncTargetIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		klog.Errorf("Failed to get SyncTargets in %q: %v", clusterName, err)
		return nil // nothing we can do here
	}

	var errs []error
	for _, obj := range cs {
		cluster := obj.(*workloadv1alpha1.SyncTarget)
		apiDomainKey := dynamiccontext.APIDomainKey(clusters.ToClusterAwareKey(clusterName, cluster.Name))
		if err := c.reconcile(ctx, apiExport, apiDomainKey, logicalcluster.From(cluster), cluster.Name); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.NewAggregate(errs)
}

func (c *APIReconciler) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	apiSet, ok := c.apiSets[key]
	return apiSet, ok, nil
}
