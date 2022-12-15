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

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	schedulingv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	schedulingv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

const (
	ControllerName = "kcp-workload-apiexport-create"

	DefaultLocationName = "default"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	locationInformer schedulingv1alpha1informers.LocationClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(export *apisv1alpha1.APIExport, duration time.Duration) {
			key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
			if err != nil {
				runtime.HandleError(err)
				return
			}
			queue.AddAfter(key, duration)
		},

		kcpClusterClient: kcpClusterClient,

		apiExportsLister: apiExportInformer.Lister(),
		apiBindingLister: apiBindingInformer.Lister(),
		syncTargetLister: syncTargetInformer.Lister(),
		locationLister:   locationInformer.Lister(),
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

	kcpClusterClient kcpclientset.ClusterInterface

	syncTargetLister workloadv1alpha1listers.SyncTargetClusterLister
	apiExportsLister apisv1alpha1listers.APIExportClusterLister
	apiBindingLister apisv1alpha1listers.APIBindingClusterLister
	locationLister   schedulingv1alpha1listers.LocationClusterLister
}

// enqueue adds the logical cluster to the queue.
func (c *controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	key = clusterName.String()
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	if logObj, ok := obj.(logging.Object); ok {
		logger = logging.WithObject(logger, logObj)
	}
	logger.V(2).Info(fmt.Sprintf("queueing ClusterWorkspace because of %T", obj))
	c.queue.Add(key)
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
	logger.V(1).Info("processing key")

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
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.Name(key)

	syncTargets, err := c.syncTargetLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		logger.Error(err, "failed to list clusters for workspace")
		return err
	}
	if len(syncTargets) == 0 {
		logger.V(3).Info("no clusters found for workspace. Not creating APIExport and APIBinding")
		return nil
	}

	// check that export exists, and create it if not
	export, err := c.apiExportsLister.Cluster(clusterName).Get(reconcilerapiexport.TemporaryComputeServiceExportName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		export = &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: reconcilerapiexport.TemporaryComputeServiceExportName,
				Annotations: map[string]string{
					logicalcluster.AnnotationKey:                   clusterName.String(),
					workloadv1alpha1.ComputeAPIExportAnnotationKey: "true",
				},
			},
			Spec: apisv1alpha1.APIExportSpec{},
		}
		logger = logging.WithObject(logger, export)
		logger.Info("creating APIExport")
		export, err = c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create APIExport")
			return err
		}
	}

	if value, found := export.Annotations[workloadv1alpha1.AnnotationSkipDefaultObjectCreation]; found && value == "true" {
		return nil
	}

	// check that location exists, and create it if not
	_, err = c.locationLister.Cluster(clusterName).Get(DefaultLocationName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if apierrors.IsNotFound(err) {
		location := &schedulingv1alpha1.Location{
			ObjectMeta: metav1.ObjectMeta{
				Name:        DefaultLocationName,
				Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName.String()},
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
		logger = logging.WithObject(logger, location)
		logger.Info("creating Location")
		_, err = c.kcpClusterClient.Cluster(clusterName.Path()).SchedulingV1alpha1().Locations().Create(ctx, location, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			logger.Error(err, "failed to create Location")
			return err
		}
	}

	// check that binding exists, and create it if not
	bindings, err := c.apiBindingLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		logger.Error(err, "failed to list APIBindings")
		return err
	}
	for _, binding := range bindings {
		if binding.Spec.Reference.Export == nil {
			continue
		}
		if binding.Spec.Reference.Export.Path != "" {
			continue
		}
		if binding.Spec.Reference.Export.Name != reconcilerapiexport.TemporaryComputeServiceExportName {
			continue
		}
		logging.WithObject(logger, binding).V(3).Info("APIBinding found pointing to APIExport")
		return nil // binding found
	}

	// bind to local export
	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: reconcilerapiexport.TemporaryComputeServiceExportName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:                   clusterName.String(),
				workloadv1alpha1.ComputeAPIExportAnnotationKey: "true",
			},
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Name: reconcilerapiexport.TemporaryComputeServiceExportName,
				},
			},
		},
	}
	logger = logging.WithObject(logger, binding)
	logger.V(2).Info("creating APIBinding")
	_, err = c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})

	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to create APIBinding")
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

	logger.WithValues("patch", string(patchData)).V(2).Info("patching APIExport")
	_, err = c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIExports().Patch(ctx, export.Name, types.MergePatchType, patchData, metav1.PatchOptions{})
	return err
}
