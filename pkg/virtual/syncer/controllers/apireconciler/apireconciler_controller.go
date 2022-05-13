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
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	apiresourcelistersv1alpha1 "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	tenancylistersv1alpha1 "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	controllerName = "kcp-virtual-syncer-api-reconciler"
	byWorkspace    = controllerName + "-byWorkspace" // will go away with scoping
)

// NewAPIReconciler returns a new controller which reconciles APIResourceImport resources
// and delegates the corresponding WorkloadClusterAPI management to the given WorkloadClusterAPIManager.
func NewAPIReconciler(
	kcpClusterClient kcpclient.ClusterInterface,
	workloadClusterInformer tenancyv1alpha1.WorkloadClusterInformer,
	negotiatedAPIResourceInformer apiresourceinformer.NegotiatedAPIResourceInformer,
	createAPIDefinition apidefinition.CreateAPIDefinitionFunc,
) (*APIReconciler, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &APIReconciler{
		kcpClusterClient: kcpClusterClient,

		workloadClusterLister:  workloadClusterInformer.Lister(),
		workloadClusterIndexer: workloadClusterInformer.Informer().GetIndexer(),

		negotiatedAPIResourceLister:  negotiatedAPIResourceInformer.Lister(),
		negotiatedAPIResourceIndexer: negotiatedAPIResourceInformer.Informer().GetIndexer(),

		queue: queue,

		createAPIDefinition: createAPIDefinition,

		apiSets: map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet{},
	}

	if err := workloadClusterInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorksapce,
	}); err != nil {
		return nil, err
	}

	if err := negotiatedAPIResourceInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorksapce,
	}); err != nil {
		return nil, err
	}

	workloadClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueWorkloadCluster(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueWorkloadCluster(obj)
		},
	})

	negotiatedAPIResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueNegotiatedAPIResource(obj)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueNegotiatedAPIResource(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueNegotiatedAPIResource(obj)
		},
	})

	return c, nil
}

// APIReconciler is a controller that reconciles APIResourceImport resources
// and delegates the corresponding WorkloadClusterAPI management to the given WorkloadClusterAPIManager.
type APIReconciler struct {
	kcpClusterClient kcpclient.ClusterInterface

	workloadClusterLister  tenancylistersv1alpha1.WorkloadClusterLister
	workloadClusterIndexer cache.Indexer

	negotiatedAPIResourceLister  apiresourcelistersv1alpha1.NegotiatedAPIResourceLister
	negotiatedAPIResourceIndexer cache.Indexer

	queue workqueue.RateLimitingInterface

	createAPIDefinition apidefinition.CreateAPIDefinitionFunc

	mutex   sync.RWMutex // protects the map, not the values!
	apiSets map[dynamiccontext.APIDomainKey]apidefinition.APIDefinitionSet
}

func (c *APIReconciler) enqueueWorkloadCluster(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	clusterName, name := clusters.SplitClusterAwareKey(key)
	resources, err := c.negotiatedAPIResourceIndexer.ByIndex(byWorkspace, clusterName.String())
	for _, obj := range resources {
		r := obj.(*apiresourcev1alpha1.NegotiatedAPIResource)

		resourceKey := name + "::" + clusters.ToClusterAwareKey(clusterName, r.Name)

		klog.V(2).Infof("Queueing NegotiatedAPIResource %s|%s for workload cluster %s", clusterName, r.Name, name)
		c.queue.Add(resourceKey)
	}
}

func (c *APIReconciler) enqueueNegotiatedAPIResource(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	clusterName, name := clusters.SplitClusterAwareKey(key)
	workloadClusters, err := c.workloadClusterIndexer.ByIndex(byWorkspace, clusterName.String())
	for _, obj := range workloadClusters {
		wc := obj.(*workloadv1alpha1.WorkloadCluster)

		resourceKey := wc.Name + "::" + clusters.ToClusterAwareKey(clusterName, name)

		klog.V(2).Infof("Queueing NegotiatedAPIResource %s|%s for workload cluster %s", clusterName, name, wc.Name)
		c.queue.Add(resourceKey)
	}
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
	comps := strings.SplitN(key, "::", 2)
	if len(comps) != 2 {
		return fmt.Errorf("invalid key %q", key)
	}
	workloadClusterName, resourceKey := comps[0], comps[1]
	clusterName, resourceName := clusters.SplitClusterAwareKey(resourceKey)
	apiDomainKey := dynamiccontext.APIDomainKey(clusters.ToClusterAwareKey(clusterName, workloadClusterName))

	_, err := c.workloadClusterLister.Get(clusters.ToClusterAwareKey(clusterName, workloadClusterName))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		c.mutex.Lock()
		defer c.mutex.Unlock()

		if _, found := c.apiSets[apiDomainKey]; found {
			klog.V(3).Infof("Workload cluster %s|%s not found. Removing resource %s.", clusterName, workloadClusterName, resourceName)
			delete(c.apiSets, apiDomainKey)
		} else {
			klog.V(4).Infof("Workload cluster %s|%s not found. No need to remove resource %s.", clusterName, workloadClusterName, resourceName)
		}

		return nil
	}

	resource, err := c.negotiatedAPIResourceLister.Get(resourceKey)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	shouldRemove := false
	var reason string
	if apierrors.IsNotFound(err) {
		shouldRemove = true
		reason = "not found"
	} else if !resource.IsConditionTrue(apiresourcev1alpha1.Published) {
		reason = "not published"
	}
	if shouldRemove {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		oldSet, ok := c.apiSets[apiDomainKey]
		if !ok {
			return nil
		}
		newSet := make(apidefinition.APIDefinitionSet, len(oldSet))
		resourceGVR := resourceNameToGVR(resourceName)
		for gvr, v := range oldSet {
			if gvr == resourceGVR {
				klog.V(3).Infof("NegotiatedAPIResource %s|%s is %s. Removing resource from workload cluster %s.", clusterName, resourceName, reason, workloadClusterName)
				continue
			}
			newSet[gvr] = v
		}

		c.apiSets[apiDomainKey] = newSet

		return nil
	}

	// both resource and workload cluster exist and resource is published. Upsert APIDefinition
	klog.V(3).Infof("Upserting resource %s|%s for workload cluster %s", clusterName, resourceName, workloadClusterName)

	c.mutex.Lock()
	defer c.mutex.Unlock()
	oldSet, foundOld := c.apiSets[apiDomainKey]
	newSet := make(apidefinition.APIDefinitionSet, len(oldSet)+1)
	if !foundOld {
		for _, api := range internalAPIs {
			def, err := c.createAPIDefinition(clusterName, api)
			if err != nil {
				klog.Errorf("Failed to create APIDefinition for %s|%s: %v", clusterName, resourceName, err)
				continue // nothing we can do, skip it
			}
			newSet[schema.GroupVersionResource{
				Group:    api.GroupVersion.Group,
				Version:  api.GroupVersion.Version,
				Resource: api.Plural,
			}] = def
		}
	}
	resourceGVR := resourceNameToGVR(resourceName)
	for gvr, v := range oldSet {
		if gvr == resourceGVR {
			continue
		}
		newSet[gvr] = v
	}
	def, err := c.createAPIDefinition(clusterName, &resource.Spec.CommonAPIResourceSpec)
	if err != nil {
		klog.Errorf("Failed to create APIDefinition for %s|%s: %v", clusterName, resourceName, err)
		return nil // nothing we can do
	}
	newSet[resourceGVR] = def

	c.apiSets[apiDomainKey] = newSet

	return nil
}

func (c *APIReconciler) GetAPIDefinitionSet(key dynamiccontext.APIDomainKey) (apidefinition.APIDefinitionSet, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	apiSet, ok := c.apiSets[key]
	return apiSet, ok
}

func resourceNameToGVR(key string) schema.GroupVersionResource {
	parts := strings.SplitN(key, ".", 3)
	resource, version, group := parts[0], parts[1], parts[2]
	if group == "core" {
		group = ""
	}
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
}
