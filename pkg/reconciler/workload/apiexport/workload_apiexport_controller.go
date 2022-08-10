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

package apiexport

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apiresourcelisters "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	controllerName = "kcp-workload-apiexport"
	byWorkspace    = controllerName + "-byWorkspace" // will go away with scoping

	// TemporaryComputeServiceExportName is a temporary singleton name of compute service exports.
	TemporaryComputeServiceExportName = "kubernetes"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclient.Interface,
	apiExportInformer apisinformers.APIExportInformer,
	apiResourceSchemaInformer apisinformers.APIResourceSchemaInformer,
	negotiatedAPIResourceInformer apiresourceinformer.NegotiatedAPIResourceInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(export *apisv1alpha1.APIExport, duration time.Duration) {
			key := clusters.ToClusterAwareKey(logicalcluster.From(export), export.Name)
			queue.AddAfter(key, duration)
		},
		kcpClusterClient:             kcpClusterClient,
		apiExportsLister:             apiExportInformer.Lister(),
		apiExportsIndexer:            apiExportInformer.Informer().GetIndexer(),
		apiResourceSchemaLister:      apiResourceSchemaInformer.Lister(),
		apiResourceSchemaIndexer:     apiResourceSchemaInformer.Informer().GetIndexer(),
		negotiatedAPIResourceLister:  negotiatedAPIResourceInformer.Lister(),
		negotiatedAPIResourceIndexer: negotiatedAPIResourceInformer.Informer().GetIndexer(),
	}

	if err := c.apiResourceSchemaIndexer.AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := negotiatedAPIResourceInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := apiExportInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	apiExportInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *apisv1alpha1.APIExport:
				return t.Name == TemporaryComputeServiceExportName
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
			DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
		},
	})

	negotiatedAPIResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueNegotiatedAPIResource(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueNegotiatedAPIResource(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueNegotiatedAPIResource(obj) },
	})

	return c, nil
}

// controller reconciles APIResourceSchemas and the "workloads" APIExport in a
// API negotiation domain based on NegotiatedAPIResources:
// - it creates APIResourceSchemas for every NegotiatedAPIResource in the workspace
// - it maintains the list of latest resource schemas in the APIExport
// - it deletes APIResourceSchemas that have no NegotiatedAPIResource in the workspace anymore, but are listed in the APIExport.
//
// It does NOT create APIExport.
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*apisv1alpha1.APIExport, time.Duration)

	kcpClusterClient kcpclient.Interface

	apiExportsLister             apislisters.APIExportLister
	apiExportsIndexer            cache.Indexer
	apiResourceSchemaLister      apislisters.APIResourceSchemaLister
	apiResourceSchemaIndexer     cache.Indexer
	negotiatedAPIResourceLister  apiresourcelisters.NegotiatedAPIResourceLister
	negotiatedAPIResourceIndexer cache.Indexer
}

func (c *controller) enqueueNegotiatedAPIResource(obj interface{}) {
	resource, ok := obj.(*apiresourcev1alpha1.NegotiatedAPIResource)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a NegotiatedAPIResource, but is %T", obj))
		return
	}

	clusterName := logicalcluster.From(resource)
	key := clusters.ToClusterAwareKey(clusterName, TemporaryComputeServiceExportName)
	if _, err := c.apiExportsLister.Get(clusters.ToClusterAwareKey(clusterName, TemporaryComputeServiceExportName)); errors.IsNotFound(err) {
		return // it's gone
	} else if err != nil {
		runtime.HandleError(fmt.Errorf("failed to get APIExport %s|%s: %w", clusterName, TemporaryComputeServiceExportName, err))
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logging.WithObject(logger, resource).V(2).Info("queueing APIExport due to NegotiatedAPIResource")
	c.queue.Add(key)
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), key)
	logger.V(2).Info("queueing APIExport")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	obj, err := c.apiExportsLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	return nil
}
