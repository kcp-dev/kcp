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
	"encoding/json"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

const (
	controllerName = "kcp-workload-apiexport-create"

	byWorkspace = controllerName + "-byWorkspace" // will go away with scoping

	DefaultLocationName = "default"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclient.Interface,
	syncTargetInformer workloadinformers.SyncTargetInformer,
	apiExportInformer apisinformers.APIExportInformer,
	apiBindingInformer apisinformers.APIBindingInformer,
	locationInformer schedulinginformers.LocationInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(binding *apisv1alpha1.APIExport, duration time.Duration) {
			key := clusters.ToClusterAwareKey(logicalcluster.From(binding), binding.Name)
			queue.AddAfter(key, duration)
		},

		kcpClusterClient: kcpClusterClient,

		apiExportsLister:  apiExportInformer.Lister(),
		apiExportsIndexer: apiExportInformer.Informer().GetIndexer(),

		apiBindingLister:  apiBindingInformer.Lister(),
		apiBindingIndexer: apiBindingInformer.Informer().GetIndexer(),

		syncTargetLister:  syncTargetInformer.Lister(),
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),

		locationLister: locationInformer.Lister(),
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
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

	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	locationInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *schedulingv1alpha1.Location:
				return t.Name == DefaultLocationName
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
		},
	})

	return c, nil
}

// controller reconciles watches SyncTargets and creates a APIExport and self-binding
// as soon as there is one in a workspace.
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(*apisv1alpha1.APIExport, time.Duration)

	kcpClusterClient kcpclient.Interface

	syncTargetLister  workloadlisters.SyncTargetLister
	syncTargetIndexer cache.Indexer

	apiExportsLister  apislisters.APIExportLister
	apiExportsIndexer cache.Indexer

	apiBindingLister  apislisters.APIBindingLister
	apiBindingIndexer cache.Indexer

	locationLister schedulinglisters.LocationLister
}

// enqueue adds the logical cluster to the queue.
func (c *controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

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

	syncTargets, err := c.syncTargetIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		klog.Errorf("Failed to list clusters for workspace %q: %v", clusterName.String(), err)
		return err
	}
	if len(syncTargets) == 0 {
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
		export, err = c.kcpClusterClient.ApisV1alpha1().APIExports().Create(logicalcluster.WithCluster(ctx, clusterName), export, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			klog.Errorf("Failed to create export %s|%s: %v", clusterName, reconcilerapiexport.TemporaryComputeServiceExportName, err)
			return err
		}
	}

	if value, found := export.Annotations[workloadv1alpha1.AnnotationSkipDefaultObjectCreation]; found && value == "true" {
		return nil
	}

	// check that location exists, and create it if not
	_, err = c.locationLister.Get(clusters.ToClusterAwareKey(clusterName, DefaultLocationName))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		klog.Infof("Creating location %s|%s", clusterName, DefaultLocationName)
		location := &schedulingv1alpha1.Location{
			ObjectMeta: metav1.ObjectMeta{
				Name: DefaultLocationName,
			},
			Spec: schedulingv1alpha1.LocationSpec{
				Resource: schedulingv1alpha1.GroupVersionResource{
					Group:    "workload.kcp.dev",
					Version:  "v1alpha1",
					Resource: "synctargets",
				},
				InstanceSelector: &metav1.LabelSelector{},
			},
		}
		_, err = c.kcpClusterClient.SchedulingV1alpha1().Locations().Create(logicalcluster.WithCluster(ctx, clusterName), location, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			klog.Errorf("Failed to create location %s|%s: %v", clusterName, DefaultLocationName, err)
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
	_, err = c.kcpClusterClient.ApisV1alpha1().APIBindings().Create(logicalcluster.WithCluster(ctx, clusterName), binding, metav1.CreateOptions{})

	if err != nil && !apierrors.IsAlreadyExists(err) {
		klog.Errorf("Failed to create apibinding %s|%s: %v", clusterName, reconcilerapiexport.TemporaryComputeServiceExportName, err)
		return err
	}

	// patch the apiexport, so we do not create the location/apibinding again even if it is deleted.
	exportPatch := map[string]interface{}{}
	expectedAnnotations := map[string]interface{}{
		workloadv1alpha1.AnnotationSkipDefaultObjectCreation: "true",
	}
	if err := unstructured.SetNestedField(exportPatch, expectedAnnotations, "metadata", "annotations"); err != nil {
		return err
	}
	patchData, err := json.Marshal(exportPatch)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Patching apiexport %s|%s with patch %s", clusterName, export.Name, string(patchData))
	_, err = c.kcpClusterClient.ApisV1alpha1().APIExports().Patch(logicalcluster.WithCluster(ctx, clusterName), export.Name, types.MergePatchType, patchData, metav1.PatchOptions{})
	return err
}
