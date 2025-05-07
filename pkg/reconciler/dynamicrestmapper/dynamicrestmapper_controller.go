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
	"encoding/json"
	"fmt"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	builtinschemas "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-dynamicrestmapper"
)

// Describes which handler triggered enqueueLogicalCluster.
type ctrlOp string

const (
	opNone   ctrlOp = ""
	opCreate ctrlOp = "Create"
	opUpdate ctrlOp = "Update"
	opDelete ctrlOp = "Delete"
)

// queueItem is marshaled to and from JSON and used as a queue key.
type queueItem struct {
	ClusterName         logicalcluster.Name
	ClusterResourceName string
	Op                  ctrlOp

	ToRemove apibinding.ResourceBindingsAnnotation
	ToAdd    apibinding.ResourceBindingsAnnotation
}

func NewController(
	ctx context.Context,
	state *DynamicRESTMapper,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		state: state,

		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
				apisv1alpha1.Resource("apiexports"),
				apiExportInformer.Informer().GetIndexer(),
				globalAPIExportInformer.Informer().GetIndexer(),
				path,
				name,
			)
		},

		getLogicalCluster: func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().Cluster(clusterName).Get(name)
		},

		getAPIResourceSchema: informer.NewScopedGetterWithFallback(apiResourceSchemaInformer.Lister(), globalAPIResourceSchemaInformer.Lister()),

		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},

		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).Get(name)
		},
	}

	objOrTombstone := func(obj any) any {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = tombstone.Obj
		}
		return obj
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(nil, objOrTombstone(obj).(*corev1alpha1.LogicalCluster), opCreate)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueLogicalCluster(objOrTombstone(oldObj).(*corev1alpha1.LogicalCluster),
				objOrTombstone(newObj).(*corev1alpha1.LogicalCluster), opUpdate)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(objOrTombstone(obj).(*corev1alpha1.LogicalCluster), nil, opDelete)
		},
	})

	return c, nil
}

type Controller struct {
	state *DynamicRESTMapper

	queue workqueue.TypedRateLimitingInterface[string]

	getLogicalCluster    func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)
	getAPIExportByPath   func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getAPIBinding        func(clusterName logicalcluster.Name, name string) (*apibinding.APIBinding, error)
}

// When we detect a new LogicalCluster, we add builtinGVKRs mappings to it.
// Since they are always the same, we stash them away to be reused.
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

func getResourceBindingsAnnJSON(lc *corev1alpha1.LogicalCluster) string {
	const jsonEmptyObj = "{}"

	if lc == nil {
		return jsonEmptyObj
	}

	ann := lc.Annotations[apibinding.ResourceBindingsAnnotationKey]
	if ann == "" {
		ann = jsonEmptyObj
	}

	return ann
}

func diffResourceBindingsAnn(oldAnn, newAnn apibinding.ResourceBindingsAnnotation) (toRemove, toAdd apibinding.ResourceBindingsAnnotation) {
	toRemove = make(apibinding.ResourceBindingsAnnotation)
	toAdd = make(apibinding.ResourceBindingsAnnotation)

	for k, v := range newAnn {
		if _, hasInOld := oldAnn[k]; !hasInOld {
			toAdd[k] = v
		}
	}

	for k, v := range oldAnn {
		if _, hasInNew := newAnn[k]; !hasInNew {
			toRemove[k] = v
		}
	}

	return
}

func (c *Controller) enqueueLogicalCluster(oldObj *corev1alpha1.LogicalCluster, newObj *corev1alpha1.LogicalCluster, op ctrlOp) {
	oldBoundResourcesAnnStr := getResourceBindingsAnnJSON(oldObj)
	newBoundResourcesAnnStr := getResourceBindingsAnnJSON(newObj)

	if op == opUpdate && oldBoundResourcesAnnStr == newBoundResourcesAnnStr {
		// Nothing to do.
		return
	}

	oldBoundResourcesAnn, err := apibinding.UnmarshalResourceBindingsAnnotation(oldBoundResourcesAnnStr)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	newBoundResourcesAnn, err := apibinding.UnmarshalResourceBindingsAnnotation(newBoundResourcesAnnStr)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	alt := func(a, b *corev1alpha1.LogicalCluster) *corev1alpha1.LogicalCluster {
		if a != nil {
			return a
		} else {
			return b
		}
	}
	it := queueItem{
		ClusterName:         logicalcluster.From(alt(oldObj, newObj)),
		ClusterResourceName: alt(oldObj, newObj).Name,
		Op:                  op,
	}
	it.ToRemove, it.ToAdd = diffResourceBindingsAnn(oldBoundResourcesAnn, newBoundResourcesAnn)

	keyBytes, err := json.Marshal(&it)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	key := string(keyBytes)

	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).
		V(4).Info("queueing ResourceBindingsAnnotation patch because of LogicalCluster")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	var it queueItem
	if err := json.Unmarshal([]byte(key), &it); err != nil {
		logger.Error(err, "failed to unmarshal queue key")
		c.queue.Forget(key)
		return true
	}

	if err := c.process(ctx, key, it); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) gatherGVKRsForCRD(crd *apiextensionsv1.CustomResourceDefinition) []typeMeta {
	if crd == nil {
		return nil
	}
	gvkrs := make([]typeMeta, len(crd.Status.StoredVersions))
	for i, crdVersion := range crd.Status.StoredVersions {
		gvkrs[i] = newTypeMeta(
			crd.Spec.Group,
			crdVersion,
			crd.Status.AcceptedNames.Kind,
			crd.Status.AcceptedNames.Singular,
			crd.Status.AcceptedNames.Plural,
			resourceScopeToRESTScope(crd.Spec.Scope),
		)
	}
	return gvkrs
}

func (c *Controller) gatherGVKRsForAPIBinding(apiBinding *apisv1alpha1.APIBinding) ([]typeMeta, error) {
	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiBinding).Path()
	}

	apiExport, err := c.getAPIExportByPath(apiExportPath, apiBinding.Spec.Reference.Export.Name)
	if err != nil {
		return nil, err
	}

	var gvkrs []typeMeta

	for _, resourceSchema := range apiExport.Spec.Resources {
		sch, err := c.getAPIResourceSchema(logicalcluster.From(apiExport), resourceSchema.Schema)
		if err != nil {
			return nil, err
		}

		for _, schVersion := range sch.Spec.Versions {
			gvkrs = append(gvkrs, newTypeMeta(
				sch.Spec.Group,
				schVersion.Name,
				sch.Spec.Names.Kind,
				sch.Spec.Names.Singular,
				sch.Spec.Names.Plural,
				resourceScopeToRESTScope(sch.Spec.Scope),
			))
		}
	}

	return gvkrs, nil
}

func (c *Controller) gatherGVKRsForBoundResource(clusterName logicalcluster.Name, resourceGroup string, boundResourceLock apibinding.Lock) ([]typeMeta, error) {
	if boundResourceLock.CRD {
		crd, err := c.getCRD(clusterName, resourceGroup)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve CRD %s: %v", resourceGroup, err)
		}

		return c.gatherGVKRsForCRD(crd), nil
	}

	apiBinding, err := c.getAPIBinding(clusterName, boundResourceLock.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve APIBinding %s: %v", resourceGroup, err)
	}

	return c.gatherGVKRsForAPIBinding(apiBinding)
}

func (c *Controller) gatherGVKRsForMappedBoundResource(clusterName logicalcluster.Name, resourceGroup string, boundResourceLock apibinding.Lock) ([]typeMeta, error) {
	gvkrs, err := c.state.ForCluster(clusterName).getGVKRs(schema.ParseGroupResource(resourceGroup))
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return gvkrs, nil
}

func (c *Controller) process(ctx context.Context, key string, item queueItem) error {
	logger := logging.WithQueueKey(klog.FromContext(ctx), key)

	if item.Op == opDelete {
		logger.V(4).Info("LogicalCluster was removed, removing all its mappings")
		c.state.deleteMappingsForCluster(item.ClusterName)
		return nil
	}

	if _, err := c.getLogicalCluster(item.ClusterName, item.ClusterResourceName); err != nil {
		if apierrors.IsNotFound(err) {
			c.state.deleteMappingsForCluster(item.ClusterName)
			logger.V(4).Info("LogicalCluster already deleted, skipping")
			return nil
		}
		return err
	}

	// Retrieve type meta for all detected changes in bound resources.

	type gathererFunc func(clusterName logicalcluster.Name, resourceGroup string, boundResourceLock apibinding.Lock) ([]typeMeta, error)

	gatherGVKRs := func(boundResources apibinding.ResourceBindingsAnnotation, gatherer gathererFunc) ([]typeMeta, error) {
		gatheredGVKRs := make([]typeMeta, 0, len(boundResources))
		for resourceGroup, boundResourceLock := range boundResources {
			gvkrs, err := gatherer(item.ClusterName, resourceGroup, boundResourceLock.Lock)
			if err != nil {
				return nil, err
			}
			gatheredGVKRs = append(gatheredGVKRs, gvkrs...)
		}
		return gatheredGVKRs, nil
	}

	typeMetaToRemove, err := gatherGVKRs(item.ToRemove, c.gatherGVKRsForMappedBoundResource)
	if err != nil {
		return err
	}

	typeMetaToAdd, err := gatherGVKRs(item.ToAdd, c.gatherGVKRsForBoundResource)
	if err != nil {
		return err
	}

	if item.Op == opCreate {
		// This is a new LogicalCluster, we need to add all built-in types too.
		typeMetaToAdd = append(typeMetaToAdd, builtinGVKRs...)
	}

	// Finally, store the new mappings in the RESTMapper for this LogicalCluster.

	logger.V(4).Info("applying mappings")

	c.state.ForCluster(item.ClusterName).apply(typeMetaToRemove, typeMetaToAdd)

	return nil
}
