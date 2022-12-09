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

package synctargetexports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apiresourcev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	apiresourcev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	ControllerName = "kcp-synctarget-export-controller"

	indexSyncTargetsByExport           = ControllerName + "ByExport"
	indexAPIExportsByAPIResourceSchema = ControllerName + "ByAPIResourceSchema"
)

// NewController returns a controller which update syncedResource in status based on supportedExports in spec
// of a syncTarget.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiResourceImportInformer apiresourcev1alpha1informers.APIResourceImportClusterInformer,
) (*Controller, error) {

	c := &Controller{
		queue:                workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		kcpClusterClient:     kcpClusterClient,
		syncTargetIndexer:    syncTargetInformer.Informer().GetIndexer(),
		syncTargetLister:     syncTargetInformer.Lister(),
		apiExportsIndexer:    apiExportInformer.Informer().GetIndexer(),
		apiExportLister:      apiExportInformer.Lister(),
		resourceSchemaLister: apiResourceSchemaInformer.Lister(),
		apiImportLister:      apiResourceImportInformer.Lister(),
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
		indexSyncTargetsByExport: indexSyncTargetsByExports,
	}); err != nil {
		return nil, err
	}

	if err := apiExportInformer.Informer().AddIndexers(cache.Indexers{
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemas,
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	}); err != nil {
		return nil, err
	}

	// Watch for events related to SyncTargets
	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, "") },
		UpdateFunc: func(old, obj interface{}) {
			oldCluster := old.(*workloadv1alpha1.SyncTarget)
			newCluster := obj.(*workloadv1alpha1.SyncTarget)

			// only enqueue when syncedResource or supportedAPIExported are changed.
			if !equality.Semantic.DeepEqual(oldCluster.Spec.SupportedAPIExports, newCluster.Spec.SupportedAPIExports) ||
				!equality.Semantic.DeepEqual(oldCluster.Status.SyncedResources, newCluster.Status.SyncedResources) {
				c.enqueueSyncTarget(obj, "")
			}
		},
		DeleteFunc: func(obj interface{}) {},
	})

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj, "") },
	})

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIResourceSchema(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIResourceSchema(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj) },
	})

	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.enqueueAPIResourceImport,
		UpdateFunc: func(old, obj interface{}) {
			oldImport := old.(*apiresourcev1alpha1.APIResourceImport)
			newImport := obj.(*apiresourcev1alpha1.APIResourceImport)

			// only enqueue when spec is changed.
			if oldImport.Generation != newImport.Generation {
				c.enqueueAPIResourceImport(obj)
			}
		},
		DeleteFunc: func(obj interface{}) {},
	})

	return c, nil
}

type Controller struct {
	queue            workqueue.RateLimitingInterface
	kcpClusterClient kcpclientset.ClusterInterface

	syncTargetIndexer    cache.Indexer
	syncTargetLister     workloadv1alpha1listers.SyncTargetClusterLister
	apiExportsIndexer    cache.Indexer
	apiExportLister      apisv1alpha1listers.APIExportClusterLister
	resourceSchemaLister apisv1alpha1listers.APIResourceSchemaClusterLister
	apiImportLister      apiresourcev1alpha1listers.APIResourceImportClusterLister
}

func (c *Controller) enqueueSyncTarget(obj interface{}, logSuffix string) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.V(2).Infof("Queueing SyncTarget %q%s", key, logSuffix)
	c.queue.Add(key)
}

func (c *Controller) enqueueAPIResourceImport(obj interface{}) {
	apiImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIResourceImport, but is %T", obj))
		return
	}

	lcluster := logicalcluster.From(apiImport)
	key := kcpcache.ToClusterAwareKey(lcluster.String(), "", apiImport.Spec.Location)

	klog.V(2).Infof("Queueing SyncTarget %q because of APIResourceImport %s", key, apiImport.Name)
	c.queue.Add(key)
}

func (c *Controller) enqueueAPIExport(obj interface{}, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	synctargets, err := c.syncTargetIndexer.ByIndex(indexSyncTargetsByExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range synctargets {
		c.enqueueSyncTarget(obj, fmt.Sprintf(" because of APIExport %s%s", key, logSuffix))
	}
}

// enqueueAPIResourceSchema maps an APIResourceSchema to APIExports for enqueuing.
func (c *Controller) enqueueAPIResourceSchema(obj interface{}) {
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

	for _, obj := range apiExports {
		c.enqueueAPIExport(obj, fmt.Sprintf(" because of APIResourceSchema %s", key))
	}
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting workers", "controller", ControllerName)
	defer klog.InfoS("Stopping workers", "controller", ControllerName)

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
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	var errs []error

	syncTarget, err := c.syncTargetLister.Cluster(cluster).Get(name)
	if err != nil {
		klog.Errorf("Failed to get syncTarget with key %q because: %v", key, err)
		return nil
	}

	klog.Infof("Processing syncTarget %q", key)

	currentSyncTarget := syncTarget.DeepCopy()

	exportReconciler := &exportReconciler{
		getAPIExport:      c.getAPIExport,
		getResourceSchema: c.getResourceSchema,
	}
	currentSyncTarget, err = exportReconciler.reconcile(ctx, currentSyncTarget)
	if err != nil {
		errs = append(errs, err)
	}

	apiCompatibleReconciler := &apiCompatibleReconciler{
		getAPIExport:           c.getAPIExport,
		getResourceSchema:      c.getResourceSchema,
		listAPIResourceImports: c.listAPIResourceImports,
	}
	currentSyncTarget, err = apiCompatibleReconciler.reconcile(ctx, currentSyncTarget)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.NewAggregate(errs)
	}

	if equality.Semantic.DeepEqual(syncTarget.Status.SyncedResources, currentSyncTarget.Status.SyncedResources) {
		return nil
	}

	oldData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		Status: workloadv1alpha1.SyncTargetStatus{
			SyncedResources: syncTarget.Status.SyncedResources,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for placement %s: %w", key, err)
	}

	newData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			UID:             syncTarget.UID,
			ResourceVersion: syncTarget.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: workloadv1alpha1.SyncTargetStatus{
			SyncedResources: currentSyncTarget.Status.SyncedResources,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for LocationDomain %s: %w", key, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		klog.Errorf("Failed to create merge patch for syncTarget %q because: %v", key, err)
		return err
	}

	clusterName := logicalcluster.From(currentSyncTarget)
	klog.V(2).Infof("Patching synctarget %s|%s with patch %s", clusterName, currentSyncTarget.Name, string(patchBytes))
	if _, err := c.kcpClusterClient.Cluster(clusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, currentSyncTarget.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status"); err != nil {
		klog.Errorf("failed to patch sync target status: %v", err)
		return err
	}

	return nil
}

func (c *Controller) getAPIExport(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
	objs, err := c.apiExportsIndexer.ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
	if err != nil {
		return nil, err
	}
	if len(objs) != 1 {
		return nil, fmt.Errorf("expected 1 APIExport for cluster %q, got %d", path, len(objs))
	}
	return objs[0].(*apisv1alpha1.APIExport), nil
}

func (c *Controller) getResourceSchema(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
	return c.resourceSchemaLister.Cluster(clusterName).Get(name)
}

func (c *Controller) listAPIResourceImports(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.APIResourceImport, error) {
	return c.apiImportLister.Cluster(clusterName).List(labels.Everything())
}
