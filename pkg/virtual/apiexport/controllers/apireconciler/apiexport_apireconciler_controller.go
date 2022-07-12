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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	ControllerName = "kcp-virtual-apiexport-api-reconciler"
	byWorkspace    = ControllerName + "-byWorkspace" // will go away with scoping
)

type CreateAPIDefinitionFunc func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string, permissionClaim *apisv1alpha1.PermissionClaim) (apidefinition.APIDefinition, error)

// NewAPIReconciler returns a new controller which reconciles APIResourceImport resources
// and delegates the corresponding SyncTargetAPI management to the given SyncTargetAPIManager.
func NewAPIReconciler(
	kcpClusterClient kcpclient.ClusterInterface,
	apiResourceSchemaInformer apisinformer.APIResourceSchemaInformer,
	apiExportInformer apisinformer.APIExportInformer,
	createAPIDefinition CreateAPIDefinitionFunc,
) (*APIReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &APIReconciler{
		kcpClusterClient: kcpClusterClient,

		apiResourceSchemaLister:  apiResourceSchemaInformer.Lister(),
		apiResourceSchemaIndexer: apiResourceSchemaInformer.Informer().GetIndexer(),

		apiExportLister:  apiExportInformer.Lister(),
		apiExportIndexer: apiExportInformer.Informer().GetIndexer(),

		queue: queue,

		createAPIDefinition: createAPIDefinition,

		apiSets: map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet{},
	}

	if err := apiExportInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIExport(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj)
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

	return c, nil
}

// APIReconciler is a controller watching APIExports and APIResourceSchemas, and updates the
// API definitions driving the virtual workspace.
type APIReconciler struct {
	kcpClusterClient kcpclient.ClusterInterface

	apiResourceSchemaLister  apislisters.APIResourceSchemaLister
	apiResourceSchemaIndexer cache.Indexer

	apiExportLister  apislisters.APIExportLister
	apiExportIndexer cache.Indexer

	queue workqueue.RateLimitingInterface

	createAPIDefinition CreateAPIDefinitionFunc

	mutex   sync.RWMutex // protects the map, not the values!
	apiSets map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet
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

	if len(exports) == 0 {
		klog.V(2).Infof("No kubernetes APIExport found for APIResourceSchema %s|%s", clusterName, name)
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
	apiDomainKey := dynamiccontext.APIDomainKey(clusterName.String() + "/" + apiExportName)

	apiExport, err := c.apiExportLister.Get(key)
	if err != nil && !apierrors.IsNotFound(err) {
		klog.Errorf("failed to get APIExport %s|%s from lister: %v", clusterName, apiExportName, err)
		return nil // nothing we can do here
	}

	return c.reconcile(ctx, apiExport, apiDomainKey)
}

func (c *APIReconciler) GetAPIDefinitionSet(_ context.Context, key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	apiSet, ok := c.apiSets[key]
	return apiSet, ok, nil
}
