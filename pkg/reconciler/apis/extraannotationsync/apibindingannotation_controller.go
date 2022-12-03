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

package extraannotationsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-api-export-extra-annotation-sync"
)

// NewController returns a new controller instance.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiExportInformer apisinformers.APIExportClusterInformer,
	apiBindingInformer apisinformers.APIBindingClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue: queue,

		kcpClusterClient: kcpClusterClient,

		apiExportsLister: apiExportInformer.Lister(),

		apiBindingsLister: apiBindingInformer.Lister(),

		getAPIBindingsByAPIExportKey: func(key string) ([]*apisv1alpha1.APIBinding, error) {
			return indexers.ByIndex[*apisv1alpha1.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingsByAPIExport, key)
		},
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger) },
	})

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
	})

	return c, nil
}

// controller continuously sync annotations with the prefix extra.api.kcp.dev from an APIExport to
// all APIBindings that bind to the APIExport. If the annotation is added to the APIExport, the controller ensures
// the existence of the annotation on all related APIBindings. If the annotaion is removed from the APIExport, the
// controller ensures the annotation is removed from all related APIBindings.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	apiExportsLister apislisters.APIExportClusterLister

	apiBindingsLister apislisters.APIBindingClusterLister

	getAPIBindingsByAPIExportKey func(key string) ([]*apisv1alpha1.APIBinding, error)
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info(fmt.Sprintf("queueing APIBinding%s", logSuffix))
	c.queue.Add(key)
}

// enqueueAPIExport enqueues maps an APIExport to APIBindings for enqueuing.
func (c *controller) enqueueAPIExport(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	bindingsForExport, err := c.getAPIBindingsByAPIExportKey(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, binding := range bindingsForExport {
		c.enqueueAPIBinding(binding, logging.WithObject(logger, obj.(*apisv1alpha1.APIExport)), " because of APIExport")
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
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}
	apiBinding, err := c.apiBindingsLister.Cluster(clusterName).Get(name)
	if errors.IsNotFound(err) {
		return nil // object deleted before we handled it
	}
	if err != nil {
		return err
	}

	if apiBinding.Spec.Reference.Export == nil {
		return nil
	}

	apiExport, err := c.apiExportsLister.Cluster(apiBinding.Spec.Reference.Export.Cluster).Get(apiBinding.Spec.Reference.Export.Name)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	patchBytes, err := syncExtraAnnotationPatch(apiExport.Annotations, apiBinding.Annotations)
	if err != nil {
		return err
	}
	if len(patchBytes) == 0 {
		return nil
	}

	_, err = c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	return err
}

func syncExtraAnnotationPatch(a1, a2 map[string]string) ([]byte, error) {
	annotationToPatch := map[string]interface{}{} // nil means to remove the key
	// Override annotations from a1 to a2
	for k, v := range a1 {
		if !strings.HasPrefix(k, apisv1alpha1.AnnotationAPIExportExtraKeyPrefix) {
			continue
		}
		if value, ok := a2[k]; !ok || v != value {
			annotationToPatch[k] = v
		}
	}

	// remove annotation on a2 if it does not exist on a1
	for k := range a2 {
		if !strings.HasPrefix(k, apisv1alpha1.AnnotationAPIExportExtraKeyPrefix) {
			continue
		}
		if _, ok := a1[k]; !ok {
			annotationToPatch[k] = nil
		}
	}

	if len(annotationToPatch) == 0 {
		return nil, nil
	}

	patch := map[string]interface{}{}
	if err := unstructured.SetNestedField(patch, annotationToPatch, "metadata", "annotations"); err != nil {
		return nil, err
	}

	return json.Marshal(patch)
}
