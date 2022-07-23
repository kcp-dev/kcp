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

package ingresssplitter

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	networkinginformers "k8s.io/client-go/informers/networking/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const controllerName = "kcp-ingress-splitter"

// NewController returns a new Controller which splits new Ingress objects
// into N virtual Ingresses labeled for each Cluster that exists at the time
// the Ingress is created.
//
// The controller can optionally aggregate the leave's status into the root
// ingress. This makes sense if the envoy side is disabled.
func NewController(
	ingressInformer networkinginformers.IngressInformer,
	serviceInformer coreinformers.ServiceInformer,
	domain string,
	aggregateLeaveStatus bool) *Controller {

	c := &Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		domain:  domain,
		tracker: newTracker(),

		ingressIndexer: ingressInformer.Informer().GetIndexer(),
		ingressLister:  ingressInformer.Lister(),

		serviceIndexer: serviceInformer.Informer().GetIndexer(),
		serviceLister:  serviceInformer.Lister(),

		aggregateLeavesStatus: aggregateLeaveStatus,
	}

	// Watch for events related to Ingresses
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) {
			ingress, ok := obj.(*networkingv1.Ingress)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
					return
				}

				ingress, ok = tombstone.Obj.(*networkingv1.Ingress)
				if !ok {
					runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
					return
				}
			}

			// If it's a deleted leaf, enqueue the root
			if rootIngressKey := rootIngressKeyFor(ingress); rootIngressKey != "" {
				c.queue.Add(rootIngressKey)
				return
			}

			// Otherwise, enqueue the leaf itself
			c.enqueue(ingress)
		},
	})

	// Watch for events related to Services
	serviceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.ingressesFromService(obj) },
		UpdateFunc: func(_, obj interface{}) { c.ingressesFromService(obj) },
		DeleteFunc: func(obj interface{}) { c.ingressesFromService(obj) },
	})

	return c
}

// The Controller struct represents an Ingress controller instance.
//  - The tracker is used to keep track of the relationship between Ingresses and services.
//  - The envoycontrolplane, contains an XDS Server and translates the ingress to Envoy
//    configuration.
type Controller struct {
	queue workqueue.RateLimitingInterface

	client kubernetes.Interface

	ingressIndexer cache.Indexer
	ingressLister  networkinglisters.IngressLister

	serviceIndexer cache.Indexer
	serviceLister  corelisters.ServiceLister

	domain  string
	tracker tracker

	aggregateLeavesStatus bool
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting workers", "controller", controllerName)
	defer klog.InfoS("Stopping workers", "controller", controllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	obj, exists, err := c.ingressIndexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to get Ingress with key %q because: %v", key, err)
		return nil
	}

	if !exists {
		klog.Infof("Object with key %q was deleted", key)
		c.tracker.deleteIngress(key)

		return nil
	}

	current := obj.(*networkingv1.Ingress)
	previous := current.DeepCopy()

	klog.Infof("Processing ingress %q", key)

	if err := c.reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous, current) {
		//TODO(jmprusi): Move to patch instead of Update.
		_, err := c.client.NetworkingV1().Ingresses(current.Namespace).Update(logicalcluster.WithCluster(ctx, logicalcluster.From(current)), current, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

// ingressesFromService enqueues all the related ingresses for a given service.
func (c *Controller) ingressesFromService(obj interface{}) {
	service := obj.(*corev1.Service)

	serviceKey, err := cache.MetaNamespaceKeyFunc(service)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	// Does that Service has any Ingress associated to?
	ingresses := c.tracker.getIngressesForService(serviceKey)

	// One Service can be referenced by 0..n Ingresses, so we need to enqueue all the related ingreses.
	for _, ingress := range ingresses.List() {
		klog.Infof("tracked service %q triggered Ingress %q reconciliation", service.Name, ingress)
		c.queue.Add(ingress)
	}
}

func rootIngressKeyFor(ingress metav1.Object) string {
	if ingress.GetLabels()[OwnedByCluster] != "" && ingress.GetLabels()[OwnedByNamespace] != "" && ingress.GetLabels()[OwnedByIngress] != "" {
		return ingress.GetLabels()[OwnedByNamespace] + "/" + clusters.ToClusterAwareKey(UnescapeClusterNameLabel(ingress.GetLabels()[OwnedByCluster]), ingress.GetLabels()[OwnedByIngress])
	}

	return ""
}
