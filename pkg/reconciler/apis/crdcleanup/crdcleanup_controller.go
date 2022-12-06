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

package crdcleanup

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	kcpapiextensionsv1informers "k8s.io/apiextensions-apiserver/pkg/client/kcp/informers/externalversions/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	ControllerName = "kcp-crdcleanup"

	DefaultIdentitySecretNamespace = "kcp-system"
)

// NewController returns a new controller for CRD cleanup
func NewController(
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		getCRD: func(clusterName tenancy.Cluster, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName.Path()).Get(name)
		},
		getAPIBindingsByBoundResourceUID: func(name string) ([]*apisv1alpha1.APIBinding, error) {
			return indexers.ByIndex[*apisv1alpha1.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByBoundResourceUID, name)
		},
		deleteCRD: func(ctx context.Context, name string) error {
			return crdClusterClient.ApiextensionsV1().CustomResourceDefinitions().Cluster(apibinding.ShadowWorkspaceName.Path()).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	indexers.AddIfNotPresentOrDie(
		apiBindingInformer.Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIBindingByBoundResourceUID: indexers.IndexAPIBindingByBoundResourceUID,
		},
	)

	crdInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
			if !ok {
				return false
			}

			return tenancy.From(crd) == apibinding.ShadowWorkspaceName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueCRD(obj)
			},
			UpdateFunc: func(_, newObj interface{}) {
				c.enqueueCRD(newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueCRD(obj)
			},
		},
	})

	apiBindingInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, newObj interface{}) {
				c.enqueueFromAPIBinding(oldObj, newObj)
			},
			DeleteFunc: func(obj interface{}) {
				c.enqueueFromAPIBinding(nil, obj)
			},
		},
	)

	return c, nil
}

// controller deletes bound CRDs when they are no longer in use by any APIBindings
type controller struct {
	queue workqueue.RateLimitingInterface

	getCRD                           func(clusterName tenancy.Cluster, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getAPIBindingsByBoundResourceUID func(name string) ([]*apisv1alpha1.APIBinding, error)
	deleteCRD                        func(ctx context.Context, name string) error
}

// enqueueCRD enqueues a CRD.
func (c *controller) enqueueCRD(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing CRD")
	c.queue.Add(key)
}

func (c *controller) enqueueFromAPIBinding(oldObj, newObj interface{}) {
	newBinding, ok := newObj.(*apisv1alpha1.APIBinding)
	if !ok {
		return
	}

	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), newBinding)

	// Looking at old and new versions in case a schema gets removed from an APIExport.
	// In that case, the last APIBinding to have the schema removed will trigger the CRD delete,
	// but only the old version will have the reference to the schema.

	uidSet := sets.String{}

	if oldObj != nil {
		oldBinding, ok := oldObj.(*apisv1alpha1.APIBinding)
		if !ok {
			return
		}

		for _, boundResource := range oldBinding.Status.BoundResources {
			uidSet.Insert(boundResource.Schema.UID)
		}
	}

	for _, boundResource := range newBinding.Status.BoundResources {
		uidSet.Insert(boundResource.Schema.UID)
	}

	for uid := range uidSet {
		key := kcpcache.ToClusterAwareKey(apibinding.ShadowWorkspaceName.String(), "", uid)
		logging.WithQueueKey(logger, key).V(2).Info("queueing CRD via APIBinding")
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
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
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}
	clusterName := tenancy.Cluster(cluster.String()) // TODO: remove this when SplitMetaClusterNamespaceKey returns a tenancy.Cluster

	obj, err := c.getCRD(clusterName, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	result, err := c.getAPIBindingsByBoundResourceUID(obj.Name)
	if err != nil {
		return err
	}

	if len(result) > 0 {
		// An APIBinding that uses this bound CRD was found. Thus don't delete.
		return nil
	}

	age := time.Since(obj.CreationTimestamp.Time)

	ageThreshold := time.Second * 10
	if age < ageThreshold {
		duration := ageThreshold - age
		logger.V(4).Info("Requeueing until CRD is older to give some time for the bindings to complete initialization", "duration", duration)
		c.queue.AddAfter(key, duration)
		return nil
	}

	logger.V(1).Info("Deleting CRD")
	if err := c.deleteCRD(ctx, obj.Name); err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	return nil
}
