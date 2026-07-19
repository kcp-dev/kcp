/*
Copyright 2025 The kcp Authors.

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

package clustercachedresourceendpointslice

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	cachev1alpha1helpers "github.com/kcp-dev/sdk/apis/cache/v1alpha1/helper"
	"github.com/kcp-dev/sdk/apis/core"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	cachev1alpha1client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/cache/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	cachev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/cache/v1alpha1"
	topologyinformers "github.com/kcp-dev/sdk/client/informers/externalversions/topology/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

const (
	ControllerName = "kcp-clustercachedresourceendpointslice"
)

// NewController returns a new controller for ClusterCachedResourceEndpointSlices.
func NewController(
	clusterCachedResourceEndpointSliceClusterInformer cachev1alpha1informers.ClusterCachedResourceEndpointSliceClusterInformer,
	globalClusterCachedResourceClusterInformer cachev1alpha1informers.ClusterCachedResourceClusterInformer,
	globalAPIExportClusterInformer apisv1alpha2informers.APIExportClusterInformer,
	partitionClusterInformer topologyinformers.PartitionClusterInformer,
	kcpClusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		listClusterCachedResourceEndpointSlices: func() ([]*cachev1alpha1.ClusterCachedResourceEndpointSlice, error) {
			return clusterCachedResourceEndpointSliceClusterInformer.Lister().List(labels.Everything())
		},
		getClusterCachedResourceEndpointSlice: func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResourceEndpointSlice, error) {
			return indexers.ByPathAndName[*cachev1alpha1.ClusterCachedResourceEndpointSlice](cachev1alpha1.Resource("clustercachedresourceendpointslice"), clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(), path, name)
		},
		getClusterCachedResource: func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error) {
			return indexers.ByPathAndName[*cachev1alpha1.ClusterCachedResource](cachev1alpha1.Resource("clustercachedresource"), globalClusterCachedResourceClusterInformer.Informer().GetIndexer(), path, name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndName[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), globalAPIExportClusterInformer.Informer().GetIndexer(), path, name)
		},
		getPartition: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error) {
			return partitionClusterInformer.Lister().Cluster(clusterName).Get(name)
		},
		getClusterCachedResourceEndpointSlicesByPartition: func(key string) ([]*cachev1alpha1.ClusterCachedResourceEndpointSlice, error) {
			list, err := clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().ByIndex(indexClusterCachedResourceEndpointSlicesByPartition, key)
			if err != nil {
				return nil, err
			}
			slices := make([]*cachev1alpha1.ClusterCachedResourceEndpointSlice, 0, len(list))
			for _, obj := range list {
				slices = append(slices, obj.(*cachev1alpha1.ClusterCachedResourceEndpointSlice))
			}
			return slices, nil
		},
		clusterCachedResourceEndpointSliceClusterInformer: clusterCachedResourceEndpointSliceClusterInformer,
		commit: committer.NewCommitter[*ClusterCachedResourceEndpointSlice, Patcher, *ClusterCachedResourceEndpointSliceSpec, *ClusterCachedResourceEndpointSliceStatus](kcpClusterClient.CacheV1alpha1().ClusterCachedResourceEndpointSlices()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = clusterCachedResourceEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.ClusterCachedResourceEndpointSlice](obj), logger, "")
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueClusterCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.ClusterCachedResourceEndpointSlice](newObj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.ClusterCachedResourceEndpointSlice](obj), logger, "")
		},
	})

	_, _ = globalClusterCachedResourceClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterCachedResource(tombstone.Obj[*cachev1alpha1.ClusterCachedResource](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterCachedResource(tombstone.Obj[*cachev1alpha1.ClusterCachedResource](obj), logger)
		},
	}))

	_, _ = globalAPIExportClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(tombstone.Obj[*apisv1alpha2.APIExport](obj), logger)
		},
	}))

	_, _ = partitionClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueuePartition(tombstone.Obj[*topologyv1alpha1.Partition](obj), logger)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueuePartition(tombstone.Obj[*topologyv1alpha1.Partition](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueuePartition(tombstone.Obj[*topologyv1alpha1.Partition](obj), logger)
		},
	}))

	return c, nil
}

type ClusterCachedResourceEndpointSlice = cachev1alpha1.ClusterCachedResourceEndpointSlice
type ClusterCachedResourceEndpointSliceSpec = cachev1alpha1.ClusterCachedResourceEndpointSliceSpec
type ClusterCachedResourceEndpointSliceStatus = cachev1alpha1.ClusterCachedResourceEndpointSliceStatus
type Patcher = cachev1alpha1client.ClusterCachedResourceEndpointSliceInterface
type Resource = committer.Resource[*ClusterCachedResourceEndpointSliceSpec, *ClusterCachedResourceEndpointSliceStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles ClusterCachedResourceEndpointSlices. It ensures that the shard endpoints are populated
// in the status of every ClusterCachedResourceEndpointSlices.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	listClusterCachedResourceEndpointSlices           func() ([]*cachev1alpha1.ClusterCachedResourceEndpointSlice, error)
	getClusterCachedResourceEndpointSlice             func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResourceEndpointSlice, error)
	getClusterCachedResource                          func(path logicalcluster.Path, name string) (*cachev1alpha1.ClusterCachedResource, error)
	getAPIExport                                      func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getPartition                                      func(cluster logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
	getClusterCachedResourceEndpointSlicesByPartition func(key string) ([]*cachev1alpha1.ClusterCachedResourceEndpointSlice, error)

	clusterCachedResourceEndpointSliceClusterInformer cachev1alpha1informers.ClusterCachedResourceEndpointSliceClusterInformer
	commit                                            CommitFunc
}

func (c *controller) enqueueClusterCachedResourceEndpointSlice(endpoints *cachev1alpha1.ClusterCachedResourceEndpointSlice, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(endpoints)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info(fmt.Sprintf("queueing ClusterCachedResourceEndpointSlice%s", logSuffix))
	c.queue.Add(key)
}

func (c *controller) enqueueClusterCachedResource(clusterCachedResource *cachev1alpha1.ClusterCachedResource, logger logr.Logger) {
	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(clusterCachedResource.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := c.clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(IndexClusterCachedResourceEndpointSliceByClusterCachedResource, path.Join(clusterCachedResource.Name).String())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(IndexClusterCachedResourceEndpointSliceByClusterCachedResource, logicalcluster.From(clusterCachedResource).Path().Join(clusterCachedResource.Name).String())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range sets.List[string](keys) {
		slice, exists, err := c.clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		} else if !exists {
			continue
		}
		c.enqueueClusterCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.ClusterCachedResourceEndpointSlice](slice), logger, " because of referenced ClusterCachedResource")
	}
}

func (c *controller) enqueueAPIExport(export *apisv1alpha2.APIExport, logger logr.Logger) {
	keys := sets.New[string]()
	for _, res := range export.Spec.Resources {
		if !cachev1alpha1helpers.IsClusterCachedResourceEndpointSliceResourceStorage(&res.Storage) {
			continue
		}
		keys.Insert(cache.ToClusterAwareKey(logicalcluster.From(export).String(), "", res.Storage.Virtual.Reference.Name))
	}
	for k := range keys {
		logger.V(4).Info("queueing ClusterCachedResourceEndpointSlice from APIExport")
		c.queue.Add(k)
	}
}

func (c *controller) enqueuePartition(obj *topologyv1alpha1.Partition, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	slices, err := c.getClusterCachedResourceEndpointSlicesByPartition(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, slice := range slices {
		c.enqueueClusterCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.ClusterCachedResourceEndpointSlice](slice), logger, " because of Partition change")
	}
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

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}
	obj, err := c.getClusterCachedResourceEndpointSlice(clusterName.Path(), name)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}

	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	globalClusterCachedResourceClusterInformer cachev1alpha1informers.ClusterCachedResourceClusterInformer,
	clusterCachedResourceEndpointSliceClusterInformer cachev1alpha1informers.ClusterCachedResourceEndpointSliceClusterInformer,
	globalAPIExportClusterInformer apisv1alpha2informers.APIExportClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(globalClusterCachedResourceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(clusterCachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName:                           indexers.IndexByLogicalClusterPathAndName,
		IndexClusterCachedResourceEndpointSliceByClusterCachedResource: IndexClusterCachedResourceEndpointSliceByClusterCachedResourceFunc,
		indexClusterCachedResourceEndpointSlicesByPartition:            indexClusterCachedResourceEndpointSlicesByPartitionFunc,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
