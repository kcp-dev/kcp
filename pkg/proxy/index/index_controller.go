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

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/index"
	indexrewriters "github.com/kcp-dev/kcp/pkg/index/rewriters"
)

const (
	controllerName = "kcp-clusterworkspace-index"

	resyncPeriod = 2 * time.Hour
)

type Index interface {
	LookupURL(path logicalcluster.Name) (url string, canonicalPath logicalcluster.Name, found bool)
}

type ClusterWorkspaceClientGetter func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclientset.ClusterInterface, error)

func NewController(
	ctx context.Context,
	clusterWorkspaceShardInformer tenancyv1alpha1informers.ClusterWorkspaceShardInformer,
	clientGetter ClusterWorkspaceClientGetter,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &Controller{
		queue: queue,

		clientGetter: clientGetter,

		clusterWorkspaceShardIndexer: clusterWorkspaceShardInformer.Informer().GetIndexer(),
		clusterWorkspaceShardLister:  clusterWorkspaceShardInformer.Lister(),

		shardClusterWorkspaceInformers: map[string]cache.SharedIndexInformer{},
		shardClusterWorkspaceStopCh:    map[string]chan struct{}{},

		state: *index.New([]index.PathRewriter{
			indexrewriters.UserRewriter,
		}),
	}

	clusterWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
			c.state.UpsertShard(shard.Name, shard.Spec.BaseURL)
			c.enqueueShard(ctx, shard)
		},
		UpdateFunc: func(old, obj interface{}) {
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
			oldShard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
			if oldShard.Spec.BaseURL == shard.Spec.BaseURL {
				return
			}
			c.stopShard(oldShard)
			c.enqueueShard(ctx, shard)
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)

			c.stopShard(shard)
		},
	})

	return c
}

// Controller watches ClusterWorkspaceShards on the root shard, and then starts informers
// for every ClusterWorkspaceShard, watching the ClusterWorkspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type Controller struct {
	queue workqueue.RateLimitingInterface

	clientGetter ClusterWorkspaceClientGetter

	clusterWorkspaceShardIndexer cache.Indexer
	clusterWorkspaceShardLister  tenancyv1alpha1listers.ClusterWorkspaceShardLister

	lock                           sync.RWMutex
	shardClusterWorkspaceInformers map[string]cache.SharedIndexInformer
	shardThisWorkspaceInformers    map[string]cache.SharedIndexInformer
	shardClusterWorkspaceStopCh    map[string]chan struct{}

	state index.State
}

// Start the controller. It does not really do anything, but to keep the shape of a normal
// controller, we keep it.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer func() {
		c.lock.Lock()
		defer c.lock.Unlock()
		for _, stopCh := range c.shardClusterWorkspaceStopCh {
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
	logger.WithValues("key", key).Info("enqueueing ClusterWorkspaceShard")

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
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	shard, err := c.clusterWorkspaceShardLister.Get(name)
	if err != nil {
		if errors.IsNotFound(err) {
			c.stopShard(shard)
			return nil
		}
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if _, found := c.shardClusterWorkspaceInformers[shard.Name]; !found {
		client, err := c.clientGetter(shard)
		if err != nil {
			return err
		}

		cwInformer := tenancyv1alpha1informers.NewClusterWorkspaceClusterInformer(client, resyncPeriod, nil)
		cwInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				ws := obj.(*tenancyv1beta1.Workspace)
				c.state.UpsertWorkspace(shard.Name, ws)
			},
			UpdateFunc: func(old, obj interface{}) {
				ws := obj.(*tenancyv1beta1.Workspace)
				c.state.UpsertWorkspace(shard.Name, ws)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				ws := obj.(*tenancyv1beta1.Workspace)
				c.state.DeleteWorkspace(shard.Name, ws)
			},
		})

		twInformer := tenancyv1alpha1informers.NewThisWorkspaceClusterInformer(client, resyncPeriod, nil)
		twInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				this := obj.(*tenancyv1alpha1.ThisWorkspace)
				c.state.UpsertThisWorkspace(shard.Name, this)
			},
			UpdateFunc: func(old, obj interface{}) {
				this := obj.(*tenancyv1alpha1.ThisWorkspace)
				c.state.UpsertThisWorkspace(shard.Name, this)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				this := obj.(*tenancyv1alpha1.ThisWorkspace)
				c.state.DeleteThisWorkspace(shard.Name, this)
			},
		})

		stopCh := make(chan struct{})
		c.shardClusterWorkspaceInformers[shard.Name] = cwInformer
		c.shardThisWorkspaceInformers[shard.Name] = twInformer
		c.shardClusterWorkspaceStopCh[shard.Name] = stopCh

		go cwInformer.Run(stopCh)

		// no need to wait. We only care about events and they arrive when they arrive.
	}

	return nil
}

func (c *Controller) stopShard(shard *tenancyv1alpha1.ClusterWorkspaceShard) {
	c.state.DeleteShard(shard.Name)

	c.lock.Lock()
	defer c.lock.Unlock()

	if stopCh, found := c.shardClusterWorkspaceStopCh[shard.Name]; found {
		close(stopCh)
	}
	delete(c.shardClusterWorkspaceStopCh, shard.Name)
	delete(c.shardClusterWorkspaceInformers, shard.Name)
	delete(c.shardThisWorkspaceInformers, shard.Name)
}

func (c *Controller) LookupURL(path logicalcluster.Name) (url string, canonicalPath logicalcluster.Name, found bool) {
	return c.state.LookupURL(path)
}
