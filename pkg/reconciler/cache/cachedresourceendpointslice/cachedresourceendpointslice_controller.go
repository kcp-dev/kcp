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

package cachedresourceendpointslice

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
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	cachev1alpha1client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/cache/v1alpha1"
	cachev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/cache/v1alpha1"
	topologyinformers "github.com/kcp-dev/sdk/client/informers/externalversions/topology/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

const (
	ControllerName = "kcp-cachedresourceendpointslice"
)

// NewController returns a new controller for CachedResourceEndpointSlices.
func NewController(
	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer,
	globalCachedResourceClusterInformer cachev1alpha1informers.CachedResourceClusterInformer,
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
		listCachedResourceEndpointSlices: func() ([]*cachev1alpha1.CachedResourceEndpointSlice, error) {
			return cachedResourceEndpointSliceClusterInformer.Lister().List(labels.Everything())
		},
		getCachedResourceEndpointSlice: func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
			return indexers.ByPathAndName[*cachev1alpha1.CachedResourceEndpointSlice](cachev1alpha1.Resource("cachedresourceendpointslice"), cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(), path, name)
		},
		getCachedResource: func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error) {
			return indexers.ByPathAndName[*cachev1alpha1.CachedResource](cachev1alpha1.Resource("cachedresource"), globalCachedResourceClusterInformer.Informer().GetIndexer(), path, name)
		},
		getPartition: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error) {
			return partitionClusterInformer.Lister().Cluster(clusterName).Get(name)
		},
		getCachedResourceEndpointSlicesByPartition: func(key string) ([]*cachev1alpha1.CachedResourceEndpointSlice, error) {
			list, err := cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().ByIndex(indexCachedResourceEndpointSlicesByPartition, key)
			if err != nil {
				return nil, err
			}
			slices := make([]*cachev1alpha1.CachedResourceEndpointSlice, 0, len(list))
			for _, obj := range list {
				slices = append(slices, obj.(*cachev1alpha1.CachedResourceEndpointSlice))
			}
			return slices, nil
		},
		cachedResourceEndpointSliceClusterInformer: cachedResourceEndpointSliceClusterInformer,
		commit: committer.NewCommitter[*CachedResourceEndpointSlice, Patcher, *CachedResourceEndpointSliceSpec, *CachedResourceEndpointSliceStatus](kcpClusterClient.CacheV1alpha1().CachedResourceEndpointSlices()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = cachedResourceEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger, "")
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](newObj), logger, "")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger, "")
		},
	})

	_, _ = globalCachedResourceClusterInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResource(tombstone.Obj[*cachev1alpha1.CachedResource](obj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResource(tombstone.Obj[*cachev1alpha1.CachedResource](obj), logger)
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

type CachedResourceEndpointSlice = cachev1alpha1.CachedResourceEndpointSlice
type CachedResourceEndpointSliceSpec = cachev1alpha1.CachedResourceEndpointSliceSpec
type CachedResourceEndpointSliceStatus = cachev1alpha1.CachedResourceEndpointSliceStatus
type Patcher = cachev1alpha1client.CachedResourceEndpointSliceInterface
type Resource = committer.Resource[*CachedResourceEndpointSliceSpec, *CachedResourceEndpointSliceStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// controller reconciles CachedResourceEndpointSlices. It ensures that the shard endpoints are populated
// in the status of every CachedResourceEndpointSlices.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	listCachedResourceEndpointSlices           func() ([]*cachev1alpha1.CachedResourceEndpointSlice, error)
	getCachedResourceEndpointSlice             func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	getCachedResource                          func(path logicalcluster.Path, name string) (*cachev1alpha1.CachedResource, error)
	getPartition                               func(cluster logicalcluster.Name, name string) (*topologyv1alpha1.Partition, error)
	getCachedResourceEndpointSlicesByPartition func(key string) ([]*cachev1alpha1.CachedResourceEndpointSlice, error)

	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer
	commit                                     CommitFunc
}

func (c *controller) enqueueCachedResourceEndpointSlice(endpoints *cachev1alpha1.CachedResourceEndpointSlice, logger logr.Logger, logSuffix string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(endpoints)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info(fmt.Sprintf("queueing CachedResourceEndpointSlice%s", logSuffix))
	c.queue.Add(key)
}

func (c *controller) enqueueCachedResource(cachedResource *cachev1alpha1.CachedResource, logger logr.Logger) {
	// binding keys by full path
	keys := sets.New[string]()
	if path := logicalcluster.NewPath(cachedResource.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
		pathKeys, err := c.cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(IndexCachedResourceEndpointSliceByCachedResource, path.Join(cachedResource.Name).String())
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		keys.Insert(pathKeys...)
	}

	clusterKeys, err := c.cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().IndexKeys(IndexCachedResourceEndpointSliceByCachedResource, logicalcluster.From(cachedResource).Path().Join(cachedResource.Name).String())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	keys.Insert(clusterKeys...)

	for _, key := range sets.List[string](keys) {
		slice, exists, err := c.cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer().GetByKey(key)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		} else if !exists {
			continue
		}
		c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](slice), logger, " because of referenced CachedResource")
	}
}

func (c *controller) enqueuePartition(obj *topologyv1alpha1.Partition, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	slices, err := c.getCachedResourceEndpointSlicesByPartition(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, slice := range slices {
		c.enqueueCachedResourceEndpointSlice(tombstone.Obj[*cachev1alpha1.CachedResourceEndpointSlice](slice), logger, " because of Partition change")
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
	obj, err := c.getCachedResourceEndpointSlice(clusterName.Path(), name)
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
	err = c.reconcile(ctx, obj)
	if err != nil {
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
	globalCachedResourceClusterInformer cachev1alpha1informers.CachedResourceClusterInformer,
	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(globalCachedResourceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName:             indexers.IndexByLogicalClusterPathAndName,
		IndexCachedResourceEndpointSliceByCachedResource: IndexCachedResourceEndpointSliceByCachedResourceFunc,
		indexCachedResourceEndpointSlicesByPartition:     indexCachedResourceEndpointSlicesByPartitionFunc,
	})
}
