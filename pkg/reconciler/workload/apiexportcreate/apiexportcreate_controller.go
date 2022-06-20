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

	"github.com/kcp-dev/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

const (
	controllerName = "kcp-workload-apiexport-create"

	byWorkspace = controllerName + "-byWorkspace" // will go away with scoping
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclient.ClusterInterface,
	workloadClusterInformer workloadinformers.WorkloadClusterInformer,
	apiExportInformer apisinformers.APIExportInformer,
	apiBindingInformer apisinformers.APIBindingInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:        queue,
		enqueueAfter: func(binding *apisv1alpha1.APIExport, duration time.Duration) { queue.AddAfter(binding, duration) },

		kcpClusterClient: kcpClusterClient,

		apiExportsLister:  apiExportInformer.Lister(),
		apiExportsIndexer: apiExportInformer.Informer().GetIndexer(),

		apiBindingLister:  apiBindingInformer.Lister(),
		apiBindingIndexer: apiBindingInformer.Informer().GetIndexer(),

		workloadClusterLister:  workloadClusterInformer.Lister(),
		workloadClusterIndexer: workloadClusterInformer.Informer().GetIndexer(),
	}

	if err := workloadClusterInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := apiBindingInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	apiExportInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *apisv1alpha1.APIExport:
				return t.Name == reconcilerapiexport.TemporaryComputeServiceExportName
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
		},
	})

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	workloadClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

// controller reconciles watches WorkloadClusters and creates a APIExport and self-binding
// as soon as there is one in a workspace.
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*apisv1alpha1.APIExport, time.Duration)

	kcpClusterClient kcpclient.ClusterInterface

	workloadClusterLister  workloadlisters.WorkloadClusterLister
	workloadClusterIndexer cache.Indexer

	apiExportsLister  apislisters.APIExportLister
	apiExportsIndexer cache.Indexer

	apiBindingLister  apislisters.APIBindingLister
	apiBindingIndexer cache.Indexer
}

// enqueue adds the logical cluster to the queue.
func (c *controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	name, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)

	klog.Infof("Enqueueing logical cluster %q because of %T %s", clusterName, obj, name)
	c.queue.Add(clusterName.String())
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
	clusterName := logicalcluster.New(key)

	workloadClusters, err := c.workloadClusterIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		klog.Errorf("Failed to list clusters for workspace %q: %v", clusterName.String(), err)
		return err
	}
	if len(workloadClusters) == 0 {
		klog.V(3).Infof("No clusters found for workspace %q. Not creating APIExport and APIBinding", clusterName.String())
		return nil
	}

	// check that export exists, and create it if not
	export, err := c.apiExportsLister.Get(clusters.ToClusterAwareKey(clusterName, reconcilerapiexport.TemporaryComputeServiceExportName))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		klog.Infof("Creating export %s|%s", clusterName, reconcilerapiexport.TemporaryComputeServiceExportName)
		export = &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: reconcilerapiexport.TemporaryComputeServiceExportName,
			},
			Spec: apisv1alpha1.APIExportSpec{},
		}
		export, err = c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			klog.Errorf("Failed to create export %s|%s: %v", clusterName, reconcilerapiexport.TemporaryComputeServiceExportName, err)
			return err
		}
	}

	// check that binding exists, and create it if not
	bindings, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		klog.Errorf("Failed to list bindings for workspace %q: %v", clusterName.String(), err)
		return err
	}
	for _, obj := range bindings {
		binding := obj.(*apisv1alpha1.APIBinding)
		if binding.Spec.Reference.Workspace == nil {
			continue
		}
		if binding.Spec.Reference.Workspace.Path != clusterName.String() {
			continue
		}
		if binding.Spec.Reference.Workspace.ExportName != reconcilerapiexport.TemporaryComputeServiceExportName {
			continue
		}
		klog.V(3).Infof("APIBinding %s|%s found pointing to APIExport %s|%s", clusterName, binding.Name, clusterName, export.Name)
		return nil // binding found
	}

	// bind to local export
	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: reconcilerapiexport.TemporaryComputeServiceExportName,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					ExportName: reconcilerapiexport.TemporaryComputeServiceExportName,
				},
			},
		},
	}
	klog.V(2).Infof("Creating APIBinding %s|%s", clusterName, reconcilerapiexport.TemporaryComputeServiceExportName)
	_, err = c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})

	return err
}
