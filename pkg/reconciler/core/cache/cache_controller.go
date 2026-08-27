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

package cache

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

const (
	ControllerName = "kcp-cache-registration-controller"
)

// NewController returns a new controller for Cache objects.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	localCacheInformer corev1alpha1informers.CacheClusterInformer,
	globalCacheInformer corev1alpha1informers.CacheClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		commit:           committer.NewCommitter[*Cache, Patcher, *CacheSpec, *CacheStatus](kcpClusterClient.CoreV1alpha1().Caches()),
		kcpClusterClient: kcpClusterClient,

		getGlobalCacheObj: func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Cache, error) {
			// Normally, getting from cache won't work through the informer, because we're missing the shard.
			// We'll list instead. There should be only one matching object anyway.
			objs, err := globalCacheInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}
			for _, obj := range objs {
				if obj.Name == name {
					return obj, nil
				}
			}
			return nil, apierrors.NewNotFound(corev1alpha1.Resource("caches"), name)
		},
		getLocalCacheObj: func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Cache, error) {
			return localCacheInformer.Lister().Cluster(clusterName).Get(name)
		},
		createLocalCacheObj: func(ctx context.Context, cache *corev1alpha1.Cache) (*corev1alpha1.Cache, error) {
			return kcpClusterClient.CoreV1alpha1().Caches().Cluster(configshard.SystemShardCluster.Path()).Create(ctx, cache, metav1.CreateOptions{})
		},
		updateLocalCacheObj: func(ctx context.Context, cache *corev1alpha1.Cache) (*corev1alpha1.Cache, error) {
			return kcpClusterClient.CoreV1alpha1().Caches().Cluster(configshard.SystemShardCluster.Path()).Update(ctx, cache, metav1.UpdateOptions{})
		},
		deleteLocalCacheObj: func(ctx context.Context, clusterName logicalcluster.Name, name string) error {
			return kcpClusterClient.CoreV1alpha1().Caches().Cluster(configshard.SystemShardCluster.Path()).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	_, _ = localCacheInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
		DeleteFunc: func(obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
	})
	_, _ = globalCacheInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
		DeleteFunc: func(obj interface{}) { c.enqueue(tombstone.Obj[*corev1alpha1.Cache](obj)) },
	})

	return c, nil
}

type Cache = corev1alpha1.Cache
type CacheSpec = corev1alpha1.CacheSpec
type CacheStatus = corev1alpha1.CacheStatus
type Patcher = corev1alpha1client.CacheInterface
type Resource = committer.Resource[*CacheSpec, *CacheStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

type Controller struct {
	queue  workqueue.TypedRateLimitingInterface[string]
	commit CommitFunc

	kcpClusterClient kcpclientset.ClusterInterface

	getGlobalCacheObj   func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Cache, error)
	getLocalCacheObj    func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Cache, error)
	createLocalCacheObj func(ctx context.Context, cache *corev1alpha1.Cache) (*corev1alpha1.Cache, error)
	updateLocalCacheObj func(ctx context.Context, cache *corev1alpha1.Cache) (*corev1alpha1.Cache, error)
	deleteLocalCacheObj func(ctx context.Context, clusterName logicalcluster.Name, name string) error
}

func (c *Controller) enqueue(cache *corev1alpha1.Cache) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(cache)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing Cache")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
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

func (c *Controller) process(ctx context.Context, key string) error {
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}

	var errs []error
	err = c.reconcile(ctx, clusterName, name)
	if err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
