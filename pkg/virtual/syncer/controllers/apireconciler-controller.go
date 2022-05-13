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

package controllers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	apiresourcelistersv1alpha1 "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
)

const controllerName = "kcp-virtual-syncer-api-reconciler"

// NewAPIReconciler returns a new controller which reconciles APIResourceImport resources
// and delegates the corresponding WorkloadClusterAPI management to the given WorkloadClusterAPIManager.
func NewAPIReconciler(
	apiManager syncer.WorkloadClusterAPIManager,
	kcpClusterClient kcpclient.ClusterInterface,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
	negotiatedAPIResourceInformer apiresourceinformer.NegotiatedAPIResourceInformer,
) (*APIReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &APIReconciler{
		apiWatcher:                  apiManager,
		kcpClusterClient:            kcpClusterClient,
		apiresourceImportLister:     apiResourceImportInformer.Lister(),
		negotiatedAPIResourceLister: negotiatedAPIResourceInformer.Lister(),
		queue:                       queue,
		workloadClusterAPIs:         make(map[string]syncer.WorkloadClusterAPI),
	}

	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueue(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueue(obj)
		},
	})

	return c, nil
}

// APIReconciler is a controller that reconciles APIResourceImport resources
// and delegates the corresponding WorkloadClusterAPI management to the given WorkloadClusterAPIManager.
type APIReconciler struct {
	apiWatcher                  syncer.WorkloadClusterAPIManager
	kcpClusterClient            kcpclient.ClusterInterface
	apiresourceImportLister     apiresourcelistersv1alpha1.APIResourceImportLister
	negotiatedAPIResourceLister apiresourcelistersv1alpha1.NegotiatedAPIResourceLister

	queue workqueue.RateLimitingInterface

	apiMutex            sync.RWMutex
	workloadClusterAPIs map[string]syncer.WorkloadClusterAPI
}

func (c *APIReconciler) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.V(2).Infof("Queueing %s", key)
	c.queue.Add(key)
}

func (c *APIReconciler) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *APIReconciler) Start(ctx context.Context) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting down %s controller", controllerName)

	go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())

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
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *APIReconciler) process(ctx context.Context, key string) error {
	delete := func() {
		c.apiMutex.Lock()
		defer c.apiMutex.Unlock()
		if existing, exists := c.workloadClusterAPIs[key]; exists {
			klog.V(3).Infof("Removing API %s", key)

			_ = c.apiWatcher.Remove(existing)
			delete(c.workloadClusterAPIs, key)
		}
	}

	apiResourceImport, err := c.apiresourceImportLister.Get(key)
	if errors.IsNotFound(err) {
		delete()
		return nil
	}
	if err != nil {
		return err
	}
	if !apiResourceImport.IsConditionTrue(apiresourcev1alpha1.Available) {
		delete()
		return nil
	}

	gv := apiResourceImport.Spec.CommonAPIResourceSpec.GroupVersion
	r := apiResourceImport.Spec.CommonAPIResourceSpec.CustomResourceDefinitionNames.Plural
	negotiatedAPIResourceName := r + "." + gv.Version + "."
	if gv.Group == "" {
		negotiatedAPIResourceName = negotiatedAPIResourceName + "core"
	} else {
		negotiatedAPIResourceName = negotiatedAPIResourceName + gv.Group
	}
	negotiatedAPIResource, err := c.negotiatedAPIResourceLister.Get(clusters.ToClusterAwareKey(logicalcluster.From(apiResourceImport), negotiatedAPIResourceName))
	if errors.IsNotFound(err) {
		delete()
		return nil
	}
	if err != nil {
		return err
	}
	if !negotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Published) && !negotiatedAPIResource.IsConditionTrue(apiresourcev1alpha1.Enforced) {
		delete()
		return nil
	}

	c.apiMutex.Lock()
	defer c.apiMutex.Unlock()

	api := syncer.WorkloadClusterAPI{
		LogicalClusterName: logicalcluster.From(apiResourceImport),
		Name:               apiResourceImport.Spec.Location,
		Spec:               (&negotiatedAPIResource.Spec.CommonAPIResourceSpec).DeepCopy(),
	}
	c.workloadClusterAPIs[key] = api

	klog.V(3).Infof("Upserting API %s", key)
	return c.apiWatcher.Upsert(api)
}
