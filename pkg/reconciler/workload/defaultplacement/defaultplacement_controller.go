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

package defaultplacement

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
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

const (
	controllerName = "kcp-workload-default-placement"

	byWorkspace = controllerName + "-byWorkspace" // will go away with scoping

	// DefaultPlacementName is the name of the default placement
	DefaultPlacementName = "default"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclient.Interface,
	apiBindingInformer apisinformers.APIBindingInformer,
	placementInformer schedulinginformers.PlacementInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,

		kcpClusterClient: kcpClusterClient,

		apiBindingIndexer: apiBindingInformer.Informer().GetIndexer(),

		placementLister: placementInformer.Lister(),
	}

	if err := apiBindingInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspace: indexByWorkspace,
	}); err != nil {
		return nil, err
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	placementInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *schedulingv1alpha1.Placement:
				return t.Name == DefaultPlacementName
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
		},
	})

	return c, nil
}

// controller reconciles watches apibinding and creates a default placement in the same workspace
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclient.Interface

	apiBindingIndexer cache.Indexer

	placementLister schedulinglisters.PlacementLister
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
	clusterName, _ := clusters.SplitClusterAwareKey(clusterAwareName)

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), controllerName), clusterName.String())
	if logObj, ok := obj.(logging.Object); ok {
		logger = logging.WithObject(logger, logObj)
	}
	logger.V(2).Info(fmt.Sprintf("queueing ClusterWorkspace because of %T", obj))
	c.queue.Add(clusterName.String())
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.New(key)

	// check that binding exists, and create it if not
	bindings, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		logger.Error(err, "failed to list APIBindings for ClusterWorkspace")
		return err
	}

	var workloadBinding *apisv1alpha1.APIBinding

	for _, obj := range bindings {
		binding := obj.(*apisv1alpha1.APIBinding)
		if binding.Spec.Reference.Workspace == nil {
			continue
		}
		if binding.Spec.Reference.Workspace.ExportName != reconcilerapiexport.TemporaryComputeServiceExportName {
			continue
		}

		workloadBinding = binding
		break
	}

	if workloadBinding == nil {
		// do nothing if there is not apibinding for workload.
		return nil
	}

	if value, found := workloadBinding.Annotations[workloadv1alpha1.AnnotationSkipDefaultObjectCreation]; found && value == "true" {
		return nil
	}

	_, err = c.placementLister.Get(clusters.ToClusterAwareKey(clusterName, DefaultPlacementName))
	if !apierrors.IsNotFound(err) {
		return err
	}

	// create default placement
	placement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:        DefaultPlacementName,
			Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName.String()},
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			LocationSelectors: []metav1.LabelSelector{{}},
			NamespaceSelector: &metav1.LabelSelector{},
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
			LocationWorkspace: workloadBinding.Spec.Reference.Workspace.Path,
		},
	}
	logger = logging.WithObject(logger, placement)
	logger.V(2).Info("creating Placement")
	_, err = c.kcpClusterClient.SchedulingV1alpha1().Placements().Create(logicalcluster.WithCluster(ctx, clusterName), placement, metav1.CreateOptions{})

	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to create Placement")
		return err
	}

	// patch the apibinding, so we do not create the placement again even if it is deleted.
	bindingPatch := map[string]interface{}{}
	expectedAnnotations := map[string]interface{}{
		workloadv1alpha1.AnnotationSkipDefaultObjectCreation: "true",
	}
	if err := unstructured.SetNestedField(bindingPatch, expectedAnnotations, "metadata", "annotations"); err != nil {
		return err
	}
	patchData, err := json.Marshal(bindingPatch)
	if err != nil {
		return err
	}

	logger.WithValues("patch", string(patchData)).V(2).Info("patching APIBinding")
	_, err = c.kcpClusterClient.ApisV1alpha1().APIBindings().Patch(logicalcluster.WithCluster(ctx, clusterName), workloadBinding.Name, types.MergePatchType, patchData, metav1.PatchOptions{})
	return err
}
