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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
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
	controllerName                    = "kcp-apiexport"
	indexAPIBindingsByWorkspaceExport = "apiBindingsByWorkspaceExport"
	shadowWorkspaceName               = "system:bound-crds"
)

// NewController returns a new controller for APIExport.
func NewController(
	kcpClient kcpclient.ClusterInterface,
	apiBindingInformer apisinformers.APIBindingInformer,
	apiExportInformer apisinformers.APIExportInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:              queue,
		kcpClusterClient:   kcpClient,
		apiBindingsLister:  apiBindingInformer.Lister(),
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),
		apiExportsLister:   apiExportInformer.Lister(),
		apiExportsIndexer:  apiExportInformer.Informer().GetIndexer(),
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

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
	})

	return c, nil
}

// controller reconciles APIExports. It watches APIBinding references to an export and
// records the current APIResourceSchemas of that export in its status.resourceSchemasInUse.
// It will remove those schemas from resourceSchemasInUse that are not bound or
// referenced anymore by any APIBinding.
//
// The APIBinding controller is watching status.resourceSchemasInUse and will only
// proceed with the bind process if the APIExport lists the schema in its
// status.resourceSchemasInUse.
//
// Note: there is a little race that this controller is informer based, and hence
//       APIBindings could be stale. So it might remove some schema although there
//       is a binding pointing to its spec (indirectly via the export reference).
//       The APIBinding controller will do a quorum read on the APIExport to verify
//       status.resourceSchemasInUse. But this is not enough to avoid the race.
//       The APIExport controller will eventually clean up. The worst consequence
//       is that an APIExport with empty status.resourceSchemasInUse could be
//       deleted although an APIBinding is just binding to it. We could make this
//       safer by doing some two-phase commit for binding.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient kcpclient.ClusterInterface

	apiBindingsLister  apislisters.APIBindingLister
	apiBindingsIndexer cache.Indexer
	apiExportsLister   apislisters.APIExportLister
	apiExportsIndexer  cache.Indexer
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
	b, err := c.apiBindingsLister.Get(key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	b.Spec.Reference = key

	list, err := c.apiBindingsIndexer.ByIndex(indexAPIBindingsByWorkspaceExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, apiBinding := range list {
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

	klog.Infof("processing key %q", key)

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
