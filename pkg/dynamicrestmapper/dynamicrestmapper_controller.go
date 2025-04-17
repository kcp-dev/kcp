/*
Copyright 2025 The KCP Authors.

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

package dynamicrestmapper

import (
	"context"
	"fmt"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	builtinschemas "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-dynamicrestmapper"
)

type queueItem struct {
	toRemove []typeMeta
	toAdd    []typeMeta
}

// Wrapper around TypedRateLimitingInterface so that we can operate
// on queueItem struct. Needed because it's not compliant with comparable
// constraint on T in TypedRateLimitingInterface[T].
type queue struct {
	lock      sync.Mutex
	wq        workqueue.TypedRateLimitingInterface[string]
	keyToItem map[string]queueItem
}

func newQueue() *queue {
	return &queue{
		wq: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		keyToItem: make(map[string]queueItem),
	}
}

func (q *queue) add(key string, it queueItem) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.wq.Add(key)
	q.keyToItem[key] = it
}

func (q *queue) addRateLimited(key string, it queueItem) {
	q.wq.AddRateLimited(key)
	q.keyToItem[key] = it
}

func (q *queue) done(key string) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.wq.Done(key)
}

func (q *queue) forget(key string) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.wq.Forget(key)
	delete(q.keyToItem, key)
}

func (q *queue) shutdown() {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.wq.ShutDown()
	q.keyToItem = make(map[string]queueItem)
}

func (q *queue) get() (key string, it queueItem, quit bool) {
	q.lock.Lock()
	defer q.lock.Unlock()
	key, quit = q.wq.Get()
	it = q.keyToItem[key]

	return
}

func NewController(
	ctx context.Context,
	state *DynamicRESTMapper,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	globalAPIExportInformer apisv1alpha1informers.APIExportClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) (*Controller, error) {
	c := &Controller{
		q:     newQueue(),
		state: state,

		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExport](
				apisv1alpha1.Resource("apiexports"),
				apiExportInformer.Informer().GetIndexer(),
				globalAPIExportInformer.Informer().GetIndexer(),
				path,
				name,
			)
		},

		getAPIResourceSchema: informer.NewScopedGetterWithFallback(apiResourceSchemaInformer.Lister(), globalAPIResourceSchemaInformer.Lister()),
	}

	objOrTombstone := func(obj any) any {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = tombstone.Obj
		}
		return obj
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(objOrTombstone(obj).(*corev1alpha1.LogicalCluster), false)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(objOrTombstone(obj).(*corev1alpha1.LogicalCluster), true)
		},
	})

	_, _ = crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCRD(nil, objOrTombstone(obj).(*apiextensionsv1.CustomResourceDefinition))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueCRD(
				objOrTombstone(oldObj).(*apiextensionsv1.CustomResourceDefinition),
				objOrTombstone(newObj).(*apiextensionsv1.CustomResourceDefinition),
			)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCRD(objOrTombstone(obj).(*apiextensionsv1.CustomResourceDefinition), nil)
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIBinding(objOrTombstone(obj).(*apisv1alpha1.APIBinding), false)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIBinding(objOrTombstone(newObj).(*apisv1alpha1.APIBinding), false)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIBinding(objOrTombstone(obj).(*apisv1alpha1.APIBinding), true)
		},
	})

	return c, nil
}

type Controller struct {
	state *DynamicRESTMapper

	q *queue

	getAPIExportByPath   func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

// When we detect a new LogicalCluster, we add builtinGVKRs mappings to it.
// Since they are always the same, we stash it away to be reused.
var builtinGVKRs []typeMeta

func init() {
	builtinGVKRs = make([]typeMeta, len(builtinschemas.BuiltInAPIs))
	for i := range builtinschemas.BuiltInAPIs {
		builtinGVKRs[i] = newTypeMeta(
			builtinschemas.BuiltInAPIs[i].GroupVersion.Group,
			builtinschemas.BuiltInAPIs[i].GroupVersion.Version,
			builtinschemas.BuiltInAPIs[i].Names.Kind,
			builtinschemas.BuiltInAPIs[i].Names.Singular,
			builtinschemas.BuiltInAPIs[i].Names.Plural,
			resourceScopeToRESTScope(builtinschemas.BuiltInAPIs[i].ResourceScope),
		)
	}
}

func (c *Controller) enqueueLogicalCluster(logicalcluster *corev1alpha1.LogicalCluster, deleted bool) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(logicalcluster)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	var it queueItem
	if deleted {
		it.toRemove = builtinGVKRs
	} else {
		it.toAdd = builtinGVKRs
	}

	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing LogicalCluster")
	c.q.add(key, it)
}

func (c *Controller) enqueueCRD(oldCRD *apiextensionsv1.CustomResourceDefinition, newCRD *apiextensionsv1.CustomResourceDefinition) {
	alt := func(a, b *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
		if a == nil {
			return b
		}
		return a
	}
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(alt(newCRD, oldCRD))
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	gatherGVKRs := func(crd *apiextensionsv1.CustomResourceDefinition) []typeMeta {
		if crd == nil {
			return nil
		}
		var gvkrs []typeMeta
		for _, crdVersion := range crd.Spec.Versions {
			gvkrs = append(gvkrs, newTypeMeta(
				crd.Spec.Group,
				crdVersion.Name,
				crd.Spec.Names.Kind,
				crd.Spec.Names.Plural,
				crd.Spec.Names.Singular,
				resourceScopeToRESTScope(crd.Spec.Scope),
			))
		}
		return gvkrs
	}

	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing CRD")
	c.q.add(key, queueItem{
		toRemove: gatherGVKRs(oldCRD),
		toAdd:    gatherGVKRs(newCRD),
	})
}

func (c *Controller) enqueueAPIBinding(apiBinding *apisv1alpha1.APIBinding, deleted bool) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiBinding)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiBinding).Path()
	}

	apiExport, err := c.getAPIExportByPath(apiExportPath, apiBinding.Spec.Reference.Export.Name)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	var gvkrs []typeMeta

	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		sch, err := c.getAPIResourceSchema(logicalcluster.From(apiExport), schemaName)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}

		for _, schVersion := range sch.Spec.Versions {
			gvkrs = append(gvkrs, newTypeMeta(
				sch.Spec.Group,
				schVersion.Name,
				sch.Spec.Names.Kind,
				sch.Spec.Names.Plural,
				sch.Spec.Names.Singular,
				resourceScopeToRESTScope(sch.Spec.Scope),
			))
		}
	}

	var it queueItem
	if deleted {
		it.toRemove = gvkrs
	} else {
		it.toAdd = gvkrs
	}

	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).
		Info("queueing APIResourceSchema because of APIBinding")
	c.q.add(key, it)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.q.shutdown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

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
	key, it, quit := c.q.get()
	if quit {
		return false
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.q.done(key)

	if err := c.process(ctx, key, it); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.q.addRateLimited(key, it)
		return true
	}
	c.q.forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string, item queueItem) error {
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)

	logger.V(4).Info("applying mappings")
	c.state.applyForCluster(clusterName, item.toRemove, item.toAdd)

	return nil
}
