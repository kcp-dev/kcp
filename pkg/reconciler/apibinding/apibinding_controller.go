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
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	apiextensionslisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

const (
	controllerName                     = "kcp-apibinding"
	indexAPIBindingsByWorkspaceExport  = "apiBindingsByWorkspaceExport"
	indexAPIExportsByAPIResourceSchema = "apiExportsByAPIResourceSchema"
	shadowWorkspaceName                = "system:bound-crds"
)

// NewController returns a new controller for APIBindings.
func NewController(
	crdClusterClient apiextensionclientset.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	apiBindingInformer apisinformers.APIBindingInformer,
	apiExportInformer apisinformers.APIExportInformer,
	apiResourceSchemaInformer apisinformers.APIResourceSchemaInformer,
	crdInformer apiextensionsinformers.CustomResourceDefinitionInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:                    queue,
		crdClusterClient:         crdClusterClient,
		kcpClusterClient:         kcpClusterClient,
		apiBindingsLister:        apiBindingInformer.Lister(),
		apiBindingsIndexer:       apiBindingInformer.Informer().GetIndexer(),
		apiExportsLister:         apiExportInformer.Lister(),
		apiExportsIndexer:        apiExportInformer.Informer().GetIndexer(),
		apiResourceSchemaLister:  apiResourceSchemaInformer.Lister(),
		apiResourceSchemaIndexer: apiResourceSchemaInformer.Informer().GetIndexer(),
		crdLister:                crdInformer.Lister(),
		crdIndexer:               crdInformer.Informer().GetIndexer(),
		deletedCRDTracker:        newLockedStringSet(),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj) },
	})

	if err := apiBindingInformer.Informer().AddIndexers(cache.Indexers{
		indexAPIBindingsByWorkspaceExport: indexAPIBindingByWorkspaceExport,
	}); err != nil {
		return nil, err
	}

	crdInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
			if !ok {
				return false
			}

			return crd.ClusterName == shadowWorkspaceName
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueCRD(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueCRD(obj) },
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

				c.enqueueCRD(obj)
			},
		},
	})

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIResourceSchema(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIResourceSchema(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj) },
	})

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
	})

	if err := c.apiExportsIndexer.AddIndexers(cache.Indexers{
		indexAPIExportsByAPIResourceSchema: indexAPIExportByAPIResourceSchemas,
	}); err != nil {
		return nil, fmt.Errorf("error add CRD indexes: %w", err)
	}

	return c, nil
}

// controller reconciles APIBindings. It creates and maintains CRDs associated with APIResourceSchemas that are
// referenced from APIBindings. It also watches CRDs, APIResourceSchemas, and APIExports to ensure whenever
// objects related to an APIBinding are updated, the APIBinding is reconciled.
type controller struct {
	queue workqueue.RateLimitingInterface

	crdClusterClient apiextensionclientset.ClusterInterface
	kcpClusterClient kcpclient.ClusterInterface

	apiBindingsLister        apislisters.APIBindingLister
	apiBindingsIndexer       cache.Indexer
	apiExportsLister         apislisters.APIExportLister
	apiExportsIndexer        cache.Indexer
	apiResourceSchemaLister  apislisters.APIResourceSchemaLister
	apiResourceSchemaIndexer cache.Indexer
	crdLister                apiextensionslisters.CustomResourceDefinitionLister
	crdIndexer               cache.Indexer

	deletedCRDTracker *lockedStringSet
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Queueing APIBinding %q", key)
	c.queue.Add(key)
}

// enqueueAPIExport enqueues maps an APIExport to APIBindings for enqueuing.
func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Mapping APIExport %q", key)
	bindingsForExport, err := c.apiBindingsIndexer.ByIndex(indexAPIBindingsByWorkspaceExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, apiBinding := range bindingsForExport {
		c.enqueueAPIBinding(apiBinding)
	}
}

// enqueueCRD maps a CRD to APIResourceSchema for enqueuing.
func (c *controller) enqueueCRD(obj interface{}) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a CustomResourceDefinition, but is %T", obj))
		return
	}

	if crd.Annotations[annotationSchemaClusterKey] == "" || crd.Annotations[annotationSchemaNameKey] == "" {
		return
	}

	apiResourceSchemaKey := clusters.ToClusterAwareKey(crd.Annotations[annotationSchemaClusterKey], crd.Annotations[annotationSchemaNameKey])
	apiResourceSchema, err := c.apiResourceSchemaLister.Get(apiResourceSchemaKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	c.enqueueAPIResourceSchema(apiResourceSchema)
}

// enqueueAPIResourceSchema maps an APIResourceSchema to APIExports for enqueuing.
func (c *controller) enqueueAPIResourceSchema(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	apiExports, err := c.apiExportsIndexer.ByIndex(indexAPIExportsByAPIResourceSchema, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, apiExport := range apiExports {
		c.enqueueAPIExport(apiExport)
	}
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
	namespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.apiBindingsLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(apisv1alpha1.APIBinding{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}
