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
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	utilreconciler "github.com/kcp-dev/kcp/pkg/reconciler/utils"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	cachev1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/cache/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	cachev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/cache/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-cachedresourceendpointslice"
)

// NewController returns a new controller for CachedResourceEndpointSlices.
func NewController(
	shardName string,
	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer,
	cachedResourceClusterInformer cachev1alpha1informers.CachedResourceClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
	lcClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	apiBindingClusterInformer apisv1alpha2informers.APIBindingClusterInformer,
	kcpClusterClient kcpclientset.ClusterInterface,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		listCachedResourceEndpointSlicesByCachedResource: func(cachedResource *cachev1alpha1.CachedResource) ([]*cachev1alpha1.CachedResourceEndpointSlice, error) {
			return indexers.ByIndex[*cachev1alpha1.CachedResourceEndpointSlice](
				cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(),
				byCachedResourceAndLogicalCluster,
				cachedResourceEndpointSliceByCachedResourceAndLogicalCluster(
					&cachev1alpha1.CachedResourceReference{
						Name: cachedResource.Name,
					},
					logicalcluster.From(cachedResource),
				),
			)
		},
		getCachedResourceEndpointSlice: func(clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
			return cachedResourceEndpointSliceClusterInformer.Cluster(clusterName).Lister().Get(name)
		},
		getCachedResource: func(clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResource, error) {
			return cachedResourceClusterInformer.Cluster(clusterName).Lister().Get(name)
		},
		getMyShard: func() (*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Cluster(core.RootCluster).Lister().Get(shardName)
		},
		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return lcClusterInformer.Cluster(clusterName).Lister().Get("cluster")
		},
		getAPIBinding: func(clusterName logicalcluster.Name, bindingName string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingClusterInformer.Cluster(clusterName).Lister().Get(bindingName)
		},
		cachedResourceEndpointSliceClusterInformer: cachedResourceEndpointSliceClusterInformer,
		commit: committer.NewCommitter[*CachedResourceEndpointSlice, Patcher, *CachedResourceEndpointSliceSpec, *CachedResourceEndpointSliceStatus](kcpClusterClient.CacheV1alpha1().CachedResourceEndpointSlices()),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = cachedResourceEndpointSliceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger)
		},
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueCachedResourceEndpointSlice(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResourceEndpointSlice](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSlice(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResourceEndpointSlice](obj), logger)
		},
	})

	_, _ = cachedResourceClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSliceByCachedResource(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResource](obj), logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueCachedResourceEndpointSliceByCachedResource(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResource](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourceEndpointSliceByCachedResource(utilreconciler.ObjOrTombstone[*cachev1alpha1.CachedResource](obj), logger)
		},
	})

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

	listCachedResourceEndpointSlicesByCachedResource func(cachedResource *cachev1alpha1.CachedResource) ([]*cachev1alpha1.CachedResourceEndpointSlice, error)
	getCachedResourceEndpointSlice                   func(clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	getCachedResource                                func(clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResource, error)
	getMyShard                                       func() (*corev1alpha1.Shard, error)
	getLogicalCluster                                func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding                                    func(clusterName logicalcluster.Name, bindingName string) (*apisv1alpha2.APIBinding, error)

	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer
	commit                                     CommitFunc
}

// enqueueCachedResourceEndpointSlice enqueues an CachedResourceEndpointSlice.
func (c *controller) enqueueCachedResourceEndpointSlice(endpoints *cachev1alpha1.CachedResourceEndpointSlice, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(endpoints)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info("queueing CachedResourceEndpointSlice")
	c.queue.Add(key)
}

func (c *controller) enqueueCachedResourceEndpointSliceByCachedResource(cachedResource *cachev1alpha1.CachedResource, logger logr.Logger) {
	slices, err := c.listCachedResourceEndpointSlicesByCachedResource(cachedResource)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for i := range slices {
		logger.V(4).Info("queueing CachedResourceEndpointSlice because of CachedResource")
		c.enqueueCachedResourceEndpointSlice(slices[i], logger)
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

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) (bool, error) {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}
	obj, err := c.getCachedResourceEndpointSlice(clusterName, name)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // object deleted before we handled it
		}
		return false, err
	}

	old := obj
	obj = obj.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, obj)
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

	return requeue, utilerrors.NewAggregate(errs)
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	cachedResourceEndpointSliceClusterInformer cachev1alpha1informers.CachedResourceEndpointSliceClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(cachedResourceEndpointSliceClusterInformer.Informer().GetIndexer(), cache.Indexers{
		byCachedResourceAndLogicalCluster: indexCachedResourceEndpointSliceByCachedResourceAndLogicalCluster,
	})
}
