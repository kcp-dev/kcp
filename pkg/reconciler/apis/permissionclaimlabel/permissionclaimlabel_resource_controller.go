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

package permissionclaimlabel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
)

const (
	controllerNameResource = "kcp-resource-permissionclaimlabel"
)

// NewController returns a new controller for APIBindings.
func NewResourceController(
	kcpClusterClient kcpclient.Interface,
	dynamicClusterClient dynamic.Interface,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	apiBindingInformer apisinformers.APIBindingInformer,
) (*resourceController, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerNameResource)

	c := &resourceController{
		queue:                queue,
		kcpClusterClient:     kcpClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		ddsif:                dynamicDiscoverySharedInformerFactory,

		apiBindingsLister: apiBindingInformer.Lister(),
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			obj, err := apiBindingInformer.Informer().GetIndexer().ByIndex(indexers.ByLogicalCluster, clusterName.String())
			var ret []*apisv1alpha1.APIBinding
			if err != nil {
				return ret, err
			}

			for _, o := range obj {
				binding, ok := o.(*apisv1alpha1.APIBinding)
				if !ok {
					klog.Errorf("unexpected type: %T, got %T", &apisv1alpha1.APIBinding{}, o)
				}
				ret = append(ret, binding)
			}

			return ret, nil
		},
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),
	}

	c.ddsif.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc:    func(gvr schema.GroupVersionResource, obj interface{}) { c.enqueueForResource(gvr, obj) },
		UpdateFunc: func(gvr schema.GroupVersionResource, _, obj interface{}) { c.enqueueForResource(gvr, obj) },
		DeleteFunc: nil, // Nothing to do.
	})

	return c, nil

}

// resourceController reconciles resources from the ddsif, and will determine if it needs
// its permission claim labels updated.
type resourceController struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient     kcpclient.Interface
	apiBindingsIndexer   cache.Indexer
	dynamicClusterClient dynamic.Interface
	ddsif                *informer.DynamicDiscoverySharedInformerFactory

	apiBindingsLister apislisters.APIBindingLister
	listAPIBindings   func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
}

// enqueueBindingForResource
func (c *resourceController) enqueueForResource(gvr schema.GroupVersionResource, obj interface{}) {
	// Determine APIBindings that have Permission Claims for the given GVR
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrstr := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")
	klog.V(4).InfoS("queueing resource", "key", gvrstr+"::"+key)
	c.queue.Add(gvrstr + "::" + key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *resourceController) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting controller", "controller", controllerNameResource)
	defer klog.InfoS("Shutting down controller", "controller", controllerNameResource)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *resourceController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *resourceController) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerNameResource, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *resourceController) process(ctx context.Context, key string) error {
	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		klog.Errorf("Error parsing key %q; dropping", key)
		return nil
	}
	gvrstr := parts[0]
	gvr, _ := schema.ParseResourceArg(gvrstr)
	if gvr == nil {
		klog.Errorf("Error parsing GVR %q; dropping", gvrstr)
		return nil
	}
	key = parts[1]

	inf, err := c.ddsif.ForResource(*gvr)
	if err != nil {
		return err
	}
	obj, exists, err := inf.Informer().GetIndexer().GetByKey(key)
	if err != nil {
		klog.Errorf("Error getting %q GVR %q from indexer: %v", key, gvrstr, err)
		return err
	}
	if !exists {
		klog.V(3).Infof("object %q GVR %q does not exist", key, gvrstr)
		return nil
	}
	unstr, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("object was not Unstructured, dropping: %T", obj)
		return nil
	}
	unstr = unstr.DeepCopy()

	// Get logical cluster name.
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("failed to split key %q, dropping: %v", key, err)
		return nil
	}
	lclusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)
	return c.reconcile(ctx, lclusterName, unstr, gvr)
}
