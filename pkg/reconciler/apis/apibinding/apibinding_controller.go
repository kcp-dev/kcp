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

	"github.com/go-logr/logr"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	"github.com/kcp-dev/kcp/pkg/tombstone"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha2client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/apis/v1alpha2"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/apis/v1alpha1"
)

const (
	ControllerName = "kcp-apibinding"
)

var (
	SystemBoundCRDsClusterName = logicalcluster.Name("system:bound-crds")
)

// NewController returns a new controller for APIBindings.
func NewController(
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiConversionInformer apisv1alpha1informers.APIConversionClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	globalAPIConversionInformer apisv1alpha1informers.APIConversionClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		crdClusterClient:          crdClusterClient,
		kcpClusterClient:          kcpClusterClient,
		cacheDynamicClusterClient: cacheDynamicClusterClient,

		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
		listAPIBindingsByAPIExport: func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
			// binding keys by full path
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

			bindings := make([]*apisv1alpha2.APIBinding, 0, keys.Len())
			for _, key := range sets.List[string](keys) {
				binding, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil {
					utilruntime.HandleError(err)
					continue
				} else if !exists {
					utilruntime.HandleError(fmt.Errorf("APIBinding %q does not exist", key))
					continue
				}
				bindings = append(bindings, binding.(*apisv1alpha2.APIBinding))
			}
			return bindings, nil
		},
		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		getAPIExportsBySchema: func(schema *apisv1alpha1.APIResourceSchema) ([]*apisv1alpha2.APIExport, error) {
			key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(schema)
			if err != nil {
				return nil, err
			}
			return indexers.ByIndexWithFallback[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), indexAPIExportsByAPIResourceSchema, key)
		},

		getAPIResourceSchema: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIResourceSchema, apisv1alpha1listers.APIResourceSchemaLister](apiResourceSchemaInformer.Lister(), globalAPIResourceSchemaInformer.Lister()),

		getAPIConversion: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIConversion, apisv1alpha1listers.APIConversionLister](apiConversionInformer.Lister(), globalAPIConversionInformer.Lister()),

		createCRD: func(ctx context.Context, clusterName logicalcluster.Path, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
		},
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},
		listCRDs: func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
		getLogicalCluster: func(name logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().Cluster(name).Get(corev1alpha1.LogicalClusterName)
		},
		updateLogicalCluster: func(ctx context.Context, lc *corev1alpha1.LogicalCluster) error {
			_, err := kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.From(lc).Path()).Update(ctx, lc, metav1.UpdateOptions{})
			return err
		},
		deletedCRDTracker: newLockedStringSet(),
		commit:            committer.NewCommitter[*APIBinding, Patcher, *APIBindingSpec, *APIBindingStatus](kcpClusterClient.ApisV1alpha2().APIBindings()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	// APIBinding handlers
	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIBinding(tombstone.Obj[*apisv1alpha2.APIBinding](obj), logger, "")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIBinding(tombstone.Obj[*apisv1alpha2.APIBinding](obj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIBinding(tombstone.Obj[*apisv1alpha2.APIBinding](obj), logger, "")
		},
	})

	// CRD handlers
	_, _ = crdInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crd := obj.(*apiextensionsv1.CustomResourceDefinition)
			return logicalcluster.From(crd) == SystemBoundCRDsClusterName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.enqueueCRD(tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj), logger)
			},
			UpdateFunc: func(_, obj interface{}) {
				c.enqueueCRD(tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj), logger)
			},
			DeleteFunc: func(obj interface{}) {
				crd := tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj)

				// If something deletes one of our bound CRDs, we need to keep track of it so when we're reconciling,
				// we know we need to recreate it. This set is there to fight against stale informers still seeing
				// the deleted CRD.
				c.deletedCRDTracker.Add(crd.Name)

				c.enqueueCRD(crd, logger)
			},
		},
	}))

	// APIExport handlers
	_, _ = apiExportInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
	}))
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger, "")
		},
	}))

	// APIResourceSchema handlers
	_, _ = apiResourceSchemaInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
	}))
	_, _ = globalAPIResourceSchemaInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceSchema(tombstone.Obj[*apisv1alpha1.APIResourceSchema](obj), logger, "")
		},
	}))

	// LogicalCluster handlers
	_, _ = logicalClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(tombstone.Obj[*corev1alpha1.LogicalCluster](obj), logger, "")
		},
		UpdateFunc: func(old, obj interface{}) {
			was := old.(*corev1alpha1.LogicalCluster).Annotations[ResourceBindingsAnnotationKey]
			is := obj.(*corev1alpha1.LogicalCluster).Annotations[ResourceBindingsAnnotationKey]
			if was != is {
				c.enqueueLogicalCluster(tombstone.Obj[*corev1alpha1.LogicalCluster](obj), logger, "")
			}
		},
	}))

	// APIConversion handlers
	_, _ = apiConversionInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIConversion(tombstone.Obj[*apisv1alpha1.APIConversion](obj), logger)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIConversion(tombstone.Obj[*apisv1alpha1.APIConversion](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIConversion(tombstone.Obj[*apisv1alpha1.APIConversion](obj), logger)
		},
	}))

	return c, nil
}

type APIBinding = apisv1alpha2.APIBinding
type APIBindingSpec = apisv1alpha2.APIBindingSpec
type APIBindingStatus = apisv1alpha2.APIBindingStatus
type Patcher = apisv1alpha2client.APIBindingInterface
type Resource = committer.Resource[*APIBindingSpec, *APIBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIBindings. It creates and maintains CRDs associated with APIResourceSchemas that are
// referenced from APIBindings. It also watches CRDs, APIResourceSchemas, and APIExports to ensure whenever
// objects related to an APIBinding are updated, the APIBinding is reconciled.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	crdClusterClient          kcpapiextensionsclientset.ClusterInterface
	kcpClusterClient          kcpclientset.ClusterInterface
	cacheDynamicClusterClient kcpdynamic.ClusterInterface

	listAPIBindings            func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error)
	listAPIBindingsByAPIExport func(apiExport *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error)
	getAPIBinding              func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)

	getAPIExportByPath    func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsBySchema func(schema *apisv1alpha1.APIResourceSchema) ([]*apisv1alpha2.APIExport, error)

	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)

	getAPIConversion func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIConversion, error)

	createCRD func(ctx context.Context, clusterName logicalcluster.Path, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error)
	getCRD    func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	listCRDs  func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error)

	getLogicalCluster    func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	updateLogicalCluster func(context.Context, *corev1alpha1.LogicalCluster) error

	deletedCRDTracker *lockedStringSet
	commit            CommitFunc
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(apiBinding *apisv1alpha2.APIBinding, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiBinding)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info(fmt.Sprintf("queueing APIBinding%s", logSuffix))
	c.queue.Add(key)
}

// enqueueAPIExport enqueues maps an APIExport to APIBindings for enqueuing.
func (c *controller) enqueueAPIExport(export *apisv1alpha2.APIExport, logger logr.Logger, logSuffix string) {
	bindings, err := c.listAPIBindingsByAPIExport(export)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, binding := range bindings {
		c.enqueueAPIBinding(binding, logging.WithObject(logger, export), fmt.Sprintf(" because of APIExport%s", logSuffix))
	}
}

// enqueueCRD maps a CRD to APIResourceSchema for enqueuing.
func (c *controller) enqueueCRD(crd *apiextensionsv1.CustomResourceDefinition, logger logr.Logger) {
	logger = logging.WithObject(logger, crd).WithValues(
		"groupResource", fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group),
		"established", apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established),
	)

	if crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey] == "" || crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey] == "" {
		logger.V(4).Info("skipping CRD because does not belong to an APIResourceSchema")
		return
	}

	clusterName := logicalcluster.Name(crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey])
	apiResourceSchema, err := c.getAPIResourceSchema(clusterName, crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey])
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	// this log here is kind of redundant normally. But we are seeing missing CRD update events
	// and hence stale APIBindings. So this might help to understand what's going on.
	logger.V(4).Info("queueing APIResourceSchema because of CRD", "key", kcpcache.ToClusterAwareKey(clusterName.String(), "", apiResourceSchema.Name))

	c.enqueueAPIResourceSchema(apiResourceSchema, logger, " because of CRD")
}

// enqueueAPIResourceSchema maps an APIResourceSchema to APIExports for enqueuing.
func (c *controller) enqueueAPIResourceSchema(schema *apisv1alpha1.APIResourceSchema, logger logr.Logger, logSuffix string) {
	apiExports, err := c.getAPIExportsBySchema(schema)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, export := range apiExports {
		c.enqueueAPIExport(export, logging.WithObject(logger, schema), fmt.Sprintf(" because of APIResourceSchema%s", logSuffix))
	}
}

// enqueueLogicalCluster maps LogicalClusters to APIBindings for enqueuing.
func (c *controller) enqueueLogicalCluster(lc *corev1alpha1.LogicalCluster, logger logr.Logger, logSuffix string) {
	bindings, err := c.listAPIBindings(logicalcluster.From(lc))
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, binding := range bindings {
		c.enqueueAPIBinding(binding, logging.WithObject(logger, binding), fmt.Sprintf(" because of LogicalCluster%s", logSuffix))
	}
}

func (c *controller) enqueueAPIConversion(apiConversion *apisv1alpha1.APIConversion, logger logr.Logger) {
	logger = logging.WithObject(logger, apiConversion)

	clusterName := logicalcluster.From(apiConversion)
	apiResourceSchema, err := c.getAPIResourceSchema(clusterName, apiConversion.Name)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info("queueing APIResourceSchema because of APIConversion", "key", kcpcache.ToClusterAwareKey(clusterName.String(), "", apiConversion.Name))

	c.enqueueAPIResourceSchema(apiResourceSchema, logger, "")
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}

	binding, err := c.getAPIBinding(clusterName, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Error(err, "failed to get APIBinding from lister", "cluster", clusterName)
		}

		return false, nil // nothing we can do here
	}

	old := binding
	binding = binding.DeepCopy()

	logger = logging.WithObject(logger, binding)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, binding)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: binding.ObjectMeta, Spec: &binding.Spec, Status: &binding.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) {
	// APIBinding indexers
	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})

	// APIExport indexers
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemasFunc,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemasFunc,
	})
}
