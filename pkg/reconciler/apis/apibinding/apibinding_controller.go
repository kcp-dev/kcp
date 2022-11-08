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
	kcpapiextensionsclientset "github.com/kcp-dev/apiextensions-apiserver/pkg/client/clientset/versioned"
	kcpapiextensionsv1informers "github.com/kcp-dev/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-apibinding"
)

var (
	ShadowWorkspaceName = logicalcluster.New("system:bound-crds")
)

// NewController returns a new controller for APIBindings.
func NewController(
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	kcpClusterClient kcpclient.Interface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	apiBindingInformer apisinformers.APIBindingInformer,
	apiExportInformer apisinformers.APIExportInformer,
	apiResourceSchemaInformer apisinformers.APIResourceSchemaInformer,
	cacheApiExportInformer apisinformers.APIExportInformer,
	cacheApiResourceSchemaInformer apisinformers.APIResourceSchemaInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue:                queue,
		crdClusterClient:     crdClusterClient,
		kcpClusterClient:     kcpClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		ddsif:                dynamicDiscoverySharedInformerFactory,

		apiBindingsLister: apiBindingInformer.Lister(),
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			list, err := apiBindingInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}

			var ret []*apisv1alpha1.APIBinding

			for i := range list {
				if logicalcluster.From(list[i]) != clusterName {
					continue
				}

				ret = append(ret, list[i])
			}

			return ret, nil
		},
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),

		getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
			apiExport, err := apiExportInformer.Lister().Get(client.ToClusterAwareKey(clusterName, name))
			if errors.IsNotFound(err) {
				return cacheApiExportInformer.Lister().Get(client.ToClusterAwareKey(clusterName, name))
			}
			return apiExport, err
		},
		apiExportsIndexer:      apiExportInformer.Informer().GetIndexer(),
		cacheApiExportsIndexer: cacheApiExportInformer.Informer().GetIndexer(),

		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			apiResourceSchema, err := apiResourceSchemaInformer.Lister().Get(client.ToClusterAwareKey(clusterName, name))
			if errors.IsNotFound(err) {
				return cacheApiResourceSchemaInformer.Lister().Get(client.ToClusterAwareKey(clusterName, name))
			}
			return apiResourceSchema, err
		},

		createCRD: func(ctx context.Context, clusterName logicalcluster.Name, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdClusterClient.Cluster(clusterName).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
		},
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},
		crdIndexer:        crdInformer.Informer().GetIndexer(),
		deletedCRDTracker: newLockedStringSet(),
		commit:            committer.NewCommitter[*APIBinding, *APIBindingSpec, *APIBindingStatus](kcpClusterClient.ApisV1alpha1().APIBindings()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)
	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger, "") },
	})

	if err := apiBindingInformer.Informer().AddIndexers(cache.Indexers{
		indexAPIBindingsByWorkspaceExport: indexAPIBindingsByWorkspaceExportFunc,
	}); err != nil {
		return nil, err
	}

	crdInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
			if !ok {
				return false
			}

			return logicalcluster.From(crd) == ShadowWorkspaceName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueCRD(obj, logger) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueCRD(obj, logger) },
			DeleteFunc: func(obj interface{}) {
				meta, err := meta.Accessor(obj)
				if err != nil {
					runtime.HandleError(err)
					return
				}

				// If something deletes one of our bound CRDs, we need to keep track of it so when we're reconciling,
				// we know we need to recreate it. This set is there to fight against stale informers still seeing
				// the deleted CRD.
				c.deletedCRDTracker.Add(meta.GetName())

				c.enqueueCRD(obj, logger)
			},
		},
	})

	if err := crdInformer.Informer().AddIndexers(cache.Indexers{
		indexByWorkspace: indexByWorkspaceFunc,
	}); err != nil {
		return nil, err
	}

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
	})

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
	})
	cacheApiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
	})
	cacheApiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger, "") },
	})

	if err := c.apiExportsIndexer.AddIndexers(cache.Indexers{
		indexAPIExportsByAPIResourceSchema: indexAPIExportsByAPIResourceSchemasFunc,
	}); err != nil {
		return nil, fmt.Errorf("error add CRD indexes: %w", err)
	}
	if err := c.cacheApiExportsIndexer.AddIndexers(cache.Indexers{
		indexAPIExportsByAPIResourceSchema: indexAPIExportsByAPIResourceSchemasFunc,
	}); err != nil {
		return nil, fmt.Errorf("error adding ApiExport indexes for the root shard: %w", err)
	}

	return c, nil
}

type APIBinding = apisv1alpha1.APIBinding
type APIBindingSpec = apisv1alpha1.APIBindingSpec
type APIBindingStatus = apisv1alpha1.APIBindingStatus
type Resource = committer.Resource[*APIBindingSpec, *APIBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles APIBindings. It creates and maintains CRDs associated with APIResourceSchemas that are
// referenced from APIBindings. It also watches CRDs, APIResourceSchemas, and APIExports to ensure whenever
// objects related to an APIBinding are updated, the APIBinding is reconciled.
type controller struct {
	queue workqueue.RateLimitingInterface

	crdClusterClient     kcpapiextensionsclientset.ClusterInterface
	kcpClusterClient     kcpclient.Interface
	dynamicClusterClient kcpdynamic.ClusterInterface
	ddsif                *informer.DynamicDiscoverySharedInformerFactory

	apiBindingsLister  apislisters.APIBindingLister
	listAPIBindings    func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	apiBindingsIndexer cache.Indexer

	getAPIExport           func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	apiExportsIndexer      cache.Indexer
	cacheApiExportsIndexer cache.Indexer

	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)

	createCRD  func(ctx context.Context, clusterName logicalcluster.Name, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error)
	getCRD     func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	crdIndexer cache.Indexer

	deletedCRDTracker *lockedStringSet
	commit            CommitFunc
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
func (c *controller) enqueueAPIExport(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	bindingsForExport, err := c.apiBindingsIndexer.ByIndex(indexAPIBindingsByWorkspaceExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, binding := range bindingsForExport {
		c.enqueueAPIBinding(binding, logging.WithObject(logger, obj.(*apisv1alpha1.APIExport)), fmt.Sprintf(" because of APIExport%s", logSuffix))
	}
}

// enqueueCRD maps a CRD to APIResourceSchema for enqueuing.
func (c *controller) enqueueCRD(obj interface{}, logger logr.Logger) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a CustomResourceDefinition, but is %T", obj))
		return
	}
	logger = logging.WithObject(logger, crd).WithValues(
		"groupResource", fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group),
		"established", apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established),
	)

	if crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey] == "" || crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey] == "" {
		logger.V(4).Info("skipping CRD because does not belong to an APIResourceSchema")
		return
	}

	clusterName := logicalcluster.New(crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey])
	apiResourceSchema, err := c.getAPIResourceSchema(clusterName, crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey])
	if err != nil {
		runtime.HandleError(err)
		return
	}

	// this log here is kind of redundant normally. But we are seeing missing CRD update events
	// and hence stale APIBindings. So this might help to undersand what's going on.
	logger.V(4).Info("queueing APIResourceSchema because of CRD", "key", client.ToClusterAwareKey(clusterName, apiResourceSchema.Name))

	c.enqueueAPIResourceSchema(apiResourceSchema, logger, " because of CRD")
}

// enqueueAPIResourceSchema maps an APIResourceSchema to APIExports for enqueuing.
func (c *controller) enqueueAPIResourceSchema(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	apiExports, err := c.apiExportsIndexer.ByIndex(indexAPIExportsByAPIResourceSchema, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if len(apiExports) == 0 {
		apiExports, err = c.cacheApiExportsIndexer.ByIndex(indexAPIExportsByAPIResourceSchema, key)
		if err != nil {
			runtime.HandleError(err)
			return
		}
	}

	for _, export := range apiExports {
		c.enqueueAPIExport(export, logging.WithObject(logger, obj.(*apisv1alpha1.APIResourceSchema)), fmt.Sprintf(" because of APIResourceSchema%s", logSuffix))
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

	obj, err := c.apiBindingsLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	reconcileErr := c.reconcile(ctx, obj)

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if commitError := c.commit(ctx, oldResource, newResource); commitError != nil {
		return commitError
	}

	return reconcileErr
}
