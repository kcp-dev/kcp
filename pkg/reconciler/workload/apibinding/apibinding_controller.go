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

package apibinding

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	controllerName = "kcp-workload-apibinding"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclient.ClusterInterface,
	apiBindingInformer apisinformers.APIBindingInformer,
	clusterWorkspaceInformer tenancyinformers.ClusterWorkspaceInformer,
	locationDomainInformer schedulinginformers.LocationDomainInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:            queue,
		enqueueAfter:     func(binding *apisv1alpha1.APIBinding, duration time.Duration) { queue.AddAfter(binding, duration) },
		kcpClusterClient: kcpClusterClient,

		apiBindingsLister:       apiBindingInformer.Lister(),
		apiBindingsIndexer:      apiBindingInformer.Informer().GetIndexer(),
		clusterWorkspaceLister:  clusterWorkspaceInformer.Lister(),
		clusterWorkspaceIndexer: clusterWorkspaceInformer.Informer().GetIndexer(),
		locationDomainLister:    locationDomainInformer.Lister(),
		locationDomainIndexer:   locationDomainInformer.Informer().GetIndexer(),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj) },
	})

	clusterWorkspaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch ws := obj.(type) {
			case *tenancyv1alpha1.ClusterWorkspace:
				key := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType("Workloads")
				return len(ws.Labels[key]) > 0
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueClusterWorkspace(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueClusterWorkspace(obj) },
		},
	})

	return c, nil
}

// controller watches LocationDomain annotations on the cluster workspaces and then
// creates APIBindings pointing to the "workloads" APIExport of the negotiation domain.
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*apisv1alpha1.APIBinding, time.Duration)

	kcpClusterClient kcpclient.ClusterInterface

	apiBindingsLister       apislisters.APIBindingLister
	apiBindingsIndexer      cache.Indexer
	clusterWorkspaceLister  tenancylisters.ClusterWorkspaceLister
	clusterWorkspaceIndexer cache.Indexer
	locationDomainLister    schedulinglisters.LocationDomainLister
	locationDomainIndexer   cache.Indexer
}

func (c *controller) enqueueClusterWorkspace(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Enqueueing ClusterWorkspace %q", key)

	c.queue.Add(key)
}

func (c *controller) enqueueAPIBinding(obj interface{}) {
	binding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIBinding, but is %T", obj))
		return
	}

	clusterName := logicalcluster.From(binding)
	org, ws := clusterName.Split()
	key := clusters.ToClusterAwareKey(org, ws)

	klog.Infof("Mapping APIBinding %s|%s to APIBinding %q", clusterName, binding.Name, key)

	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting down %s controller", controllerName)

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
	obj, err := c.clusterWorkspaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	obj = obj.DeepCopy()

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	return nil
}
