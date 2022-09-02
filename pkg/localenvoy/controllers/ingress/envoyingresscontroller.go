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
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	networkinginformers "k8s.io/client-go/informers/networking/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	envoycontrolplane "github.com/kcp-dev/kcp/pkg/localenvoy/controlplane"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/ingresssplitter"
)

const controllerName = "kcp-envoy-ingress-status-aggregator"

// NewController returns a new Controller which aggregates the status of the
// root ingress object and calls out to the envoy controlplane to update its
// state.
func NewController(
	kubeClient kubernetesclient.Interface,
	ingressInformer networkinginformers.IngressInformer,
	ecp *envoycontrolplane.EnvoyControlPlane, domain string) *Controller {

	c := &Controller{
		queue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),
		client: kubeClient,
		ecp:    ecp,
		domain: domain,

		ingressIndexer: ingressInformer.Informer().GetIndexer(),
		ingressLister:  ingressInformer.Lister(),
	}

	// Watch for events related to Ingresses
	ingressInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	return c
}

// The Controller struct represents an Ingress controller instance.
//  - The tracker is used to keep track of the relationship between Ingresses and services.
//  - The envoycontrolplane, contains an XDS Server and translates the ingress to Envoy
//    configuration.
type Controller struct {
	queue workqueue.RateLimitingInterface

	client kubernetesclient.Interface

	ingressIndexer cache.Indexer
	ingressLister  networkinglisters.IngressLister

	domain string

	ecp *envoycontrolplane.EnvoyControlPlane
}

func (c *Controller) enqueue(obj interface{}) {
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

	// If it's a leaf, also enqueue the root
	if rootIngressKey := rootIngressKeyFor(ingress); rootIngressKey != "" {
		c.queue.Add(rootIngressKey)
	}

	// Enqueue the key from obj (could be root or leaf)
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

	if requeue, err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		c.queue.AddAfter(key, time.Minute)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (requeue bool, err error) {
	obj, exists, err := c.ingressIndexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Failed to get Ingress with key %q because: %v", key, err)
		return true, nil
	}

	if !exists {
		klog.Infof("Object with key %q was deleted", key)

		if err := c.ecp.UpdateEnvoyConfig(ctx); err != nil {
			klog.Errorf("Error setting Envoy snapshot: %v", err)
			return true, nil
		}

		return false, nil
	}

	current := obj.(*networkingv1.Ingress)
	previous := current.DeepCopy()

	if err := c.reconcile(ctx, current); err != nil {
		return false, err
	}
	if !equality.Semantic.DeepEqual(previous, current) {
		if current.Labels[envoycontrolplane.ToEnvoyLabel] == "" {
			// If it's a root, we need to patch only status
			// TODO(jmprusi): Move to patch instead of Update.
			_, err := c.client.NetworkingV1().Ingresses(current.Namespace).UpdateStatus(logicalcluster.WithCluster(ctx, logicalcluster.From(current)), current, metav1.UpdateOptions{})
			if err != nil {
				return false, err
			}
		} else {
			// If it's a leaf, we need to patch only non-status (to set labels)
			// TODO(jmprusi): Move to patch instead of Update.
			_, err := c.client.NetworkingV1().Ingresses(current.Namespace).Update(logicalcluster.WithCluster(ctx, logicalcluster.From(current)), current, metav1.UpdateOptions{})
			if err != nil {
				return false, err
			}
		}
	}

	if err = c.ecp.UpdateEnvoyConfig(ctx); err != nil {
		klog.Errorf("failed setting Envoy snapshot: %w", err)
		return true, nil
	}

	return false, nil
}

func rootIngressKeyFor(ingress metav1.Object) string {
	if ingress.GetLabels()[ingresssplitter.OwnedByCluster] != "" && ingress.GetLabels()[ingresssplitter.OwnedByNamespace] != "" && ingress.GetLabels()[ingresssplitter.OwnedByIngress] != "" {
		return ingress.GetLabels()[ingresssplitter.OwnedByNamespace] + "/" + clusters.ToClusterAwareKey(ingresssplitter.UnescapeClusterNameLabel(ingress.GetLabels()[ingresssplitter.OwnedByCluster]), ingress.GetLabels()[ingresssplitter.OwnedByIngress])
	}

	return ""
}
