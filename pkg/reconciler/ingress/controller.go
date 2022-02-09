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

package ingress

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	envoycontrolplane "github.com/kcp-dev/kcp/pkg/envoy-controlplane"
)

const controllerName = "kcp-ingress-controller"

// NewController returns a new Controller which splits new Ingress objects
// into N virtual Ingresses labeled for each Cluster that exists at the time
// the Ingress is created.
// This controller can start an envoy control plane that exposes a XDS Server in
// a local port, and is used by an external Envoy proxy to get its configuration.
func NewController(
	kubeClient kubernetes.ClusterInterface,
	ingressInformer networkinginformers.IngressInformer,
	serviceInformer coreinformers.ServiceInformer,
	envoycontrolplane *envoycontrolplane.EnvoyControlPlane, domain string) *Controller {

	c := &Controller{
		queue:             workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		client:            kubeClient,
		envoycontrolplane: envoycontrolplane,
		domain:            domain,
		tracker:           newTracker(),

		ingressIndexer: ingressInformer.Informer().GetIndexer(),
		ingressLister:  ingressInformer.Lister(),

		serviceIndexer: serviceInformer.Informer().GetIndexer(),
		serviceLister:  serviceInformer.Lister(),
	}

	// Watch for events related to Ingresses
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
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

	client kubernetes.ClusterInterface

	ingressIndexer cache.Indexer
	ingressLister  networkinglisters.IngressLister

	serviceIndexer cache.Indexer
	serviceLister  corelisters.ServiceLister

	envoycontrolplane *envoycontrolplane.EnvoyControlPlane
	domain            string
	tracker           tracker
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

	// If enabled, starts the envoy control plane.
	if c.envoycontrolplane != nil {
		go func() {
			err := c.envoycontrolplane.Start(ctx)
			if err != nil {
				// TODO(jmprusi): Report state in a /readyz endpoint?
				panic(err)
			}
		}()
	}

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

		// An Ingress was deleted. But if it was a Leaf, we need to reconcile the root ingress
		// We remove the last segment of the key to get the root ingress key.
		//
		// Ex leaf key: default/admin#$#httpecho-vljrm
		// By removing the last segment, we get the "possible" key of the root ingress: default/admin#$#httpecho
		//
		splittedKey := strings.Split(key, "-")
		rootIngressKey := strings.Join(splittedKey[:len(splittedKey)-1], "-")

		rootIngress, exists, err := c.ingressIndexer.GetByKey(rootIngressKey)
		if err != nil {
			//TODO(jmprusi): Surface error to user.
			klog.Errorf("Error getting root ingress %q: %v", rootIngressKey, err)
			return nil
		}

		if exists {
			// We have the rootIngress object, we need to reconcile it.
			klog.Infof("Object with key %q was deleted, but root ingress %q still exists", key, rootIngressKey)
			obj = rootIngress

		} else {
			if c.envoycontrolplane != nil {
				// if EnvoyXDS is enabled, the new snaphost.
				err := c.envoycontrolplane.UpdateEnvoyConfig(ctx)
				if err != nil {
					klog.Errorf("Error setting snapshot: %v", err)
				}
			}

			// The ingress has been deleted, so we remove any ingress to service tracking.
			c.tracker.deleteIngress(key)
			return nil
		}
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
		_, err := c.client.Cluster(current.ClusterName).NetworkingV1().Ingresses(current.Namespace).Update(ctx, current, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}

	if c.envoycontrolplane != nil {
		err = c.envoycontrolplane.UpdateEnvoyConfig(ctx)
		if err != nil {
			return err
		}
	}

	return err
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
	ingresses, ok := c.tracker.getIngress(serviceKey)
	if !ok {
		klog.Info("Ignoring non-tracked service: ", service.Name)
		return
	}

	// One Service can be referenced by 0..n Ingresses, so we need to enqueue all the related ingreses.
	for _, ingress := range ingresses {
		klog.Infof("tracked service %q triggered Ingress %q reconciliation", service.Name, ingress.Name)
		c.enqueue(ingress)
	}
}
