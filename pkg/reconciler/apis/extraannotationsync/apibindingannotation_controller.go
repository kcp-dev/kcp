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
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
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

		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).Get(name)
		},

		getAPIBindingsByAPIExport: func(export *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error) {
			// APIBinding keys by full path
			keys := sets.New[string]()
			if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
				pathKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
				if err != nil {
					return nil, err
				}
				keys.Insert(pathKeys...)
			}

			clusterKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
			if err != nil {
				return nil, err
			}
			keys.Insert(clusterKeys...)

			ret := make([]*apisv1alpha1.APIBinding, 0, keys.Len())
			for _, key := range sets.List[string](keys) {
				binding, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil {
					runtime.HandleError(err)
					continue
				} else if !exists {
					runtime.HandleError(fmt.Errorf("APIBinding %q does not exist", key))
					continue
				}
				ret = append(ret, binding.(*apisv1alpha1.APIBinding))
			}

			return ret, nil
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), path, name)
		},
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})

	_, _ = apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger) },
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
	})

	return c, nil
}

// controller continuously sync annotations with the prefix extra.api.kcp.io from an APIExport to
// all APIBindings that bind to the APIExport. If the annotation is added to the APIExport, the controller ensures
// the existence of the annotation on all related APIBindings. If the annotaion is removed from the APIExport, the
// controller ensures the annotation is removed from all related APIBindings.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclientset.ClusterInterface

	getAPIBinding             func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error)
	getAPIBindingsByAPIExport func(export *apisv1alpha1.APIExport) ([]*apisv1alpha1.APIBinding, error)
	getAPIExport              func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info(fmt.Sprintf("queueing APIBinding%s", logSuffix))
	c.queue.Add(key)
}

// enqueueAPIExport enqueues maps an APIExport to APIBindings for enqueuing.
func (c *controller) enqueueAPIExport(obj interface{}, logger logr.Logger) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}

	export, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIExport, but is %T", obj))
		return
	}

	bindings, err := c.getAPIBindingsByAPIExport(export)
	if err != nil {
		runtime.HandleError(fmt.Errorf("error getting APIBindings for APIExport %s|%s: %w", logicalcluster.From(export), export.Name, err))
		return
	}

	logger = logging.WithObject(logger, obj.(*apisv1alpha1.APIExport))
	for _, binding := range bindings {
		c.enqueueAPIBinding(binding, logger, " because of APIExport")
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
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}
	apiBinding, err := c.getAPIBinding(clusterName, name)
	if apierrors.IsNotFound(err) {
		return nil // object deleted before we handled it
	}
	if err != nil {
		return err
	}

	logger = logging.WithObject(logger, apiBinding)

	if apiBinding.Spec.Reference.Export == nil {
		return nil
	}

	path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = clusterName.Path()
	}
	apiExport, err := c.getAPIExport(path, apiBinding.Spec.Reference.Export.Name)
	if apierrors.IsNotFound(err) {
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

	logger.V(2).Info("patching APIBinding extra annotations", "patch", string(patchBytes))
	_, err = c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIBindings().Patch(ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
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
