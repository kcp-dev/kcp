/*
Copyright 2021 The KCP Authors.

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

package index

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
	"github.com/kcp-dev/kcp/pkg/logging"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	controllerName = "kcp-workspace-index"

	resyncPeriod = 2 * time.Hour
)

type Index interface {
	LookupURL(path logicalcluster.Path) (url string, found bool)
}

type ClusterClientGetter func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error)

func NewController(
	ctx context.Context,
	shardInformer corev1alpha1informers.ShardInformer,
	clientGetter ClusterClientGetter,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &Controller{
		queue: queue,

		clientGetter: clientGetter,

		shardIndexer: shardInformer.Informer().GetIndexer(),
		shardLister:  shardInformer.Lister(),

		shardWorkspaceInformers:      map[string]cache.SharedIndexInformer{},
		shardLogicalClusterInformers: map[string]cache.SharedIndexInformer{},
		shardWorkspaceStopCh:         map[string]chan struct{}{},

		state: *index.New([]index.PathRewriter{
			indexrewriters.UserRewriter,
		}),
	}

	_, _ = shardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			shard := obj.(*corev1alpha1.Shard)
			c.state.UpsertShard(shard.Name, shard.Spec.BaseURL)
			c.enqueueShard(ctx, shard)
		},
		UpdateFunc: func(old, obj interface{}) {
			shard := obj.(*corev1alpha1.Shard)
			oldShard := obj.(*corev1alpha1.Shard)
			if oldShard.Spec.BaseURL == shard.Spec.BaseURL {
				return
			}
			c.stopShard(oldShard.Name)
			c.enqueueShard(ctx, shard)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			shard := obj.(*corev1alpha1.Shard)

			c.stopShard(shard.Name)
		},
	})

	return c
}

// Controller watches Shards on the root shard, and then starts informers
// for every Shard, watching the Workspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type Controller struct {
	queue workqueue.RateLimitingInterface

	clientGetter ClusterClientGetter

	shardIndexer cache.Indexer
	shardLister  corev1alpha1listers.ShardLister

	lock                         sync.RWMutex
	shardWorkspaceInformers      map[string]cache.SharedIndexInformer
	shardLogicalClusterInformers map[string]cache.SharedIndexInformer
	shardWorkspaceStopCh         map[string]chan struct{}

	state index.State
}

// Start the controller. It does not really do anything, but to keep the shape of a normal
// controller, we keep it.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer func() {
		c.lock.Lock()
		defer c.lock.Unlock()
		for _, stopCh := range c.shardWorkspaceStopCh {
			close(stopCh)
		}
	}()

	logger := klog.FromContext(ctx).WithValues("controller", controllerName)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) enqueueShard(ctx context.Context, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := klog.FromContext(ctx)
	logger.WithValues("key", key).Info("enqueueing Shard")

	c.queue.Add(key)
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
	logger.V(4).Info("processing key")

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

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	shard, err := c.shardLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(2).Info("Shard not found, stopping informers")
			c.stopShard(name)
			return nil
		}
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if _, found := c.shardWorkspaceInformers[shard.Name]; !found {
		logger.V(2).Info("Starting informers for Shard")

		client, err := c.clientGetter(shard)
		if err != nil {
			return err
		}

		wsInformer := tenancyv1alpha1informers.NewWorkspaceClusterInformer(client, resyncPeriod, nil)
		_, _ = wsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				ws := obj.(*tenancyv1alpha1.Workspace)
				c.state.UpsertWorkspace(shard.Name, ws)
			},
			UpdateFunc: func(old, obj interface{}) {
				ws := obj.(*tenancyv1alpha1.Workspace)
				c.state.UpsertWorkspace(shard.Name, ws)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				ws := obj.(*tenancyv1alpha1.Workspace)
				c.state.DeleteWorkspace(shard.Name, ws)
			},
		})

		twInformer := corev1alpha1informers.NewLogicalClusterClusterInformer(client, resyncPeriod, nil)
		_, _ = twInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				logicalCluster := obj.(*corev1alpha1.LogicalCluster)
				c.state.UpsertLogicalCluster(shard.Name, logicalCluster)
			},
			UpdateFunc: func(old, obj interface{}) {
				logicalCluster := obj.(*corev1alpha1.LogicalCluster)
				c.state.UpsertLogicalCluster(shard.Name, logicalCluster)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				logicalCluster := obj.(*corev1alpha1.LogicalCluster)
				c.state.DeleteLogicalCluster(shard.Name, logicalCluster)
			},
		})

		stopCh := make(chan struct{})
		c.shardWorkspaceInformers[shard.Name] = wsInformer
		c.shardLogicalClusterInformers[shard.Name] = twInformer
		c.shardWorkspaceStopCh[shard.Name] = stopCh

		go wsInformer.Run(stopCh)
		go twInformer.Run(stopCh)

		// no need to wait. We only care about events and they arrive when they arrive.
	}

	return nil
}

func (c *Controller) stopShard(shardName string) {
	c.state.DeleteShard(shardName)

	c.lock.Lock()
	defer c.lock.Unlock()

	if stopCh, found := c.shardWorkspaceStopCh[shardName]; found {
		close(stopCh)
	}
	delete(c.shardWorkspaceStopCh, shardName)
	delete(c.shardWorkspaceInformers, shardName)
	delete(c.shardLogicalClusterInformers, shardName)
}

func (c *Controller) LookupURL(path logicalcluster.Path) (url string, found bool) {
	r, found := c.state.LookupURL(path)
	return r.URL, found
}
