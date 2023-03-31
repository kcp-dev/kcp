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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	workloadv1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/workload/v1alpha1"
	apiresourcev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apiresource/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/workload/v1alpha1"
	apiresourcev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/apiresource/v1alpha1"
	workloadv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/workload/v1alpha1"
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
	apiExportInformer, globalAPIExportInformer apisv1alpha1informers.APIExportClusterInformer,
	apiResourceSchemaInformer, globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiResourceImportInformer apiresourcev1alpha1informers.APIResourceImportClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue:             workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		kcpClusterClient:  kcpClusterClient,
		syncTargetIndexer: syncTargetInformer.Informer().GetIndexer(),
		syncTargetLister:  syncTargetInformer.Lister(),
		apiExportsIndexer: apiExportInformer.Informer().GetIndexer(),
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			// Try local informer first
			export, err := indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), path, name)
			if err == nil {
				// Quick happy path - found it locally
				return export, nil
			}
			if !apierrors.IsNotFound(err) {
				// Unrecoverable error
				return nil, err
			}
			// Didn't find it locally - try remote
			return indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		getAPIResourceSchema: func(path logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			schema, err := apiResourceSchemaInformer.Cluster(path).Lister().Get(name)
			if apierrors.IsNotFound(err) {
				return globalAPIResourceSchemaInformer.Cluster(path).Lister().Get(name)
			}
			if err != nil {
				return nil, err
			}
			return schema, nil
		},
		apiImportLister: apiResourceImportInformer.Lister(),
		commit:          committer.NewCommitter[*SyncTarget, Patcher, *SyncTargetSpec, *SyncTargetStatus](kcpClusterClient.WorkloadV1alpha1().SyncTargets()),
	}

	if err := syncTargetInformer.Informer().AddIndexers(cache.Indexers{
		indexSyncTargetsByExport: indexSyncTargetsByExports,
	}); err != nil {
		return nil, err
	}

	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemas,
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	// Watch for events related to SyncTargets
	syncTargetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueSyncTarget(obj, logger, "") },
		UpdateFunc: func(old, obj interface{}) {
			oldCluster := old.(*workloadv1alpha1.SyncTarget)
			newCluster := obj.(*workloadv1alpha1.SyncTarget)

			// only enqueue when syncedResource or supportedAPIExported are changed.
			if !equality.Semantic.DeepEqual(oldCluster.Spec.SupportedAPIExports, newCluster.Spec.SupportedAPIExports) ||
				!equality.Semantic.DeepEqual(oldCluster.Status.SyncedResources, newCluster.Status.SyncedResources) {
				c.enqueueSyncTarget(obj, logger, "")
			}
		},
		DeleteFunc: func(obj interface{}) {},
	})

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj, logger, "") },
	})

	apiResourceSchemaInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIResourceSchema(obj, logger) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIResourceSchema(obj, logger) },
	})

	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIResourceImport(obj, logger)
		},
		UpdateFunc: func(old, obj interface{}) {
			oldImport := old.(*apiresourcev1alpha1.APIResourceImport)
			newImport := obj.(*apiresourcev1alpha1.APIResourceImport)

			// only enqueue when spec is changed.
			if oldImport.Generation != newImport.Generation {
				c.enqueueAPIResourceImport(obj, logger)
			}
		},
		DeleteFunc: func(obj interface{}) {},
	})

	return c, nil
}

type SyncTarget = workloadv1alpha1.SyncTarget
type SyncTargetSpec = workloadv1alpha1.SyncTargetSpec
type SyncTargetStatus = workloadv1alpha1.SyncTargetStatus
type Patcher = workloadv1alpha1client.SyncTargetInterface
type Resource = committer.Resource[*SyncTargetSpec, *SyncTargetStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

type Controller struct {
	queue            workqueue.RateLimitingInterface
	kcpClusterClient kcpclientset.ClusterInterface

	syncTargetIndexer    cache.Indexer
	syncTargetLister     workloadv1alpha1listers.SyncTargetClusterLister
	apiExportsIndexer    cache.Indexer
	getAPIExport         func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(path logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	apiImportLister      apiresourcev1alpha1listers.APIResourceImportClusterLister

	commit CommitFunc
}

func (c *Controller) enqueueSyncTarget(obj interface{}, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info(fmt.Sprintf("queueing SyncTarget%s", logSuffix))
	c.queue.Add(key)
}

func (c *Controller) enqueueAPIResourceImport(obj interface{}, logger logr.Logger) {
	apiImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIResourceImport, but is %T", obj))
		return
	}

	lcluster := logicalcluster.From(apiImport)
	key := kcpcache.ToClusterAwareKey(lcluster.String(), "", apiImport.Spec.Location)

	logging.WithQueueKey(logger, key).V(2).Info(fmt.Sprintf("queueing SyncTarget %q because of APIResourceImport %s", key, apiImport.Name))
	c.queue.Add(key)
}

func (c *Controller) enqueueAPIExport(obj interface{}, logger logr.Logger, logSuffix string) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}

	export, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a APIExport, but is %T", obj))
		return
	}

	// synctarget keys by full path
	keys := sets.NewString()
	if path := export.Annotations[core.LogicalClusterPathAnnotationKey]; path != "" {
		pathKeys, err := c.syncTargetIndexer.IndexKeys(indexSyncTargetsByExport, logicalcluster.NewPath(path).Join(export.Name).String())
		if err != nil {
			runtime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.syncTargetIndexer.IndexKeys(indexSyncTargetsByExport, logicalcluster.From(export).Path().Join(export.Name).String())
	if err != nil {
		runtime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range keys.List() {
		syncTarget, _, err := c.syncTargetIndexer.GetByKey(key)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		c.enqueueSyncTarget(syncTarget, logger, fmt.Sprintf(" because of APIExport %s%s", key, logSuffix))
	}
}

// enqueueAPIResourceSchema maps an APIResourceSchema to APIExports for enqueuing.
func (c *Controller) enqueueAPIResourceSchema(obj interface{}, logger logr.Logger) {
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
		c.enqueueAPIExport(obj, logger, fmt.Sprintf(" because of APIResourceSchema %s", key))
	}
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

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
	logger := klog.FromContext(ctx)
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	var errs []error

	syncTarget, err := c.syncTargetLister.Cluster(cluster).Get(name)
	if err != nil {
		logger.Error(err, "failed to get syncTarget")
		return nil
	}

	currentSyncTarget := syncTarget.DeepCopy()

	logger = logging.WithObject(logger, currentSyncTarget)
	ctx = klog.NewContext(ctx, logger)

	exportReconciler := &exportReconciler{
		getAPIExport:      c.getAPIExport,
		getResourceSchema: c.getAPIResourceSchema,
	}
	currentSyncTarget, err = exportReconciler.reconcile(ctx, currentSyncTarget)
	if err != nil {
		errs = append(errs, err)
	}

	apiCompatibleReconciler := &apiCompatibleReconciler{
		getAPIExport:           c.getAPIExport,
		getResourceSchema:      c.getAPIResourceSchema,
		listAPIResourceImports: c.listAPIResourceImports,
	}
	currentSyncTarget, err = apiCompatibleReconciler.reconcile(ctx, currentSyncTarget)
	if err != nil {
		errs = append(errs, err)
	}

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: syncTarget.ObjectMeta, Spec: &syncTarget.Spec, Status: &syncTarget.Status}
	newResource := &Resource{ObjectMeta: currentSyncTarget.ObjectMeta, Spec: &currentSyncTarget.Spec, Status: &currentSyncTarget.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return errors.NewAggregate(errs)
}

func (c *Controller) listAPIResourceImports(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.APIResourceImport, error) {
	return c.apiImportLister.Cluster(clusterName).List(labels.Everything())
}
