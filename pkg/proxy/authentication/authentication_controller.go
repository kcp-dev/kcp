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

package authentication

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/logging"
	proxyindex "github.com/kcp-dev/kcp/pkg/proxy/index"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	controllerName = "kcp-workspace-authentication-index"

	resyncPeriod = 2 * time.Hour
)

type ClusterClientGetter func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error)

// Controller watches Shards on the root shard, and then starts informers
// for every Shard, watching the Workspaces, their types and their authentication configurations on
// them. It then updates the workspace index, which maps logical clusters to their authenticators.
//
// This controller is very much inspired by the workspace index controller, but is its own thing
// because of the additional complexity of recursively resolving workspace types.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	clientGetter ClusterClientGetter

	// clusterIndex is provided by the workspace-index controller and is used to resolve workspaces
	// to shards.
	clusterIndex proxyindex.Index
	authIndex    state

	shardIndexer cache.Indexer
	shardLister  corev1alpha1listers.ShardLister

	lock                              sync.RWMutex
	shardWorkspaceTypeInformers       map[string]cache.SharedIndexInformer
	shardWorkspaceAuthConfigInformers map[string]cache.SharedIndexInformer
	shardWorkspaceStopCh              map[string]chan struct{}
}

func NewController(
	ctx context.Context,
	shardInformer corev1alpha1informers.ShardInformer,
	clientGetter ClusterClientGetter,
	clusterIndex proxyindex.Index,
) *Controller {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{
			Name: "controllerName",
		},
	)

	c := &Controller{
		queue: queue,

		clientGetter: clientGetter,
		clusterIndex: clusterIndex,
		authIndex:    *newState(ctx, clusterIndex),

		shardIndexer: shardInformer.Informer().GetIndexer(),
		shardLister:  shardInformer.Lister(),

		shardWorkspaceTypeInformers:       map[string]cache.SharedIndexInformer{},
		shardWorkspaceAuthConfigInformers: map[string]cache.SharedIndexInformer{},
		shardWorkspaceStopCh:              map[string]chan struct{}{},
	}

	_, _ = shardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			shard := obj.(*corev1alpha1.Shard)
			c.enqueueShard(ctx, shard)
		},
		UpdateFunc: func(old, obj interface{}) {
			shard := obj.(*corev1alpha1.Shard)
			oldShard := old.(*corev1alpha1.Shard)
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

// Start the controller. It does not really do anything, but to keep the shape of a normal
// controller, we keep it.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) enqueueShard(ctx context.Context, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
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
		utilruntime.HandleError(err)
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

	if _, found := c.shardWorkspaceAuthConfigInformers[shard.Name]; !found {
		logger.V(2).Info("Starting informers for Shard")

		client, err := c.clientGetter(shard)
		if err != nil {
			return err
		}

		wacInformer := tenancyv1alpha1informers.NewWorkspaceAuthenticationConfigurationClusterInformer(client, resyncPeriod, nil)
		_, _ = wacInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				wac := obj.(*tenancyv1alpha1.WorkspaceAuthenticationConfiguration)
				c.authIndex.UpsertWorkspaceAuthenticationConfiguration(wac)
			},
			UpdateFunc: func(old, obj interface{}) {
				wac := obj.(*tenancyv1alpha1.WorkspaceAuthenticationConfiguration)
				c.authIndex.UpsertWorkspaceAuthenticationConfiguration(wac)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				wac := obj.(*tenancyv1alpha1.WorkspaceAuthenticationConfiguration)
				c.authIndex.DeleteWorkspaceAuthenticationConfiguration(wac)
			},
		})

		wtInformer := tenancyv1alpha1informers.NewWorkspaceTypeClusterInformer(client, resyncPeriod, nil)
		_, _ = wtInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				wt := obj.(*tenancyv1alpha1.WorkspaceType)
				c.authIndex.UpsertWorkspaceType(wt)
			},
			UpdateFunc: func(old, obj interface{}) {
				wt := obj.(*tenancyv1alpha1.WorkspaceType)
				c.authIndex.UpsertWorkspaceType(wt)
			},
			DeleteFunc: func(obj interface{}) {
				if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = final.Obj
				}
				wt := obj.(*tenancyv1alpha1.WorkspaceType)
				c.authIndex.DeleteWorkspaceType(wt)
			},
		})

		stopCh := make(chan struct{})
		c.shardWorkspaceStopCh[shard.Name] = stopCh
		c.shardWorkspaceTypeInformers[shard.Name] = wtInformer
		c.shardWorkspaceAuthConfigInformers[shard.Name] = wacInformer

		go wacInformer.Run(stopCh)
		go wtInformer.Run(stopCh)

		// no need to wait. We only care about events and they arrive when they arrive.
	}

	return nil
}

func (c *Controller) stopShard(shardName string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if stopCh, found := c.shardWorkspaceStopCh[shardName]; found {
		close(stopCh)
	}
	delete(c.shardWorkspaceStopCh, shardName)
	delete(c.shardWorkspaceTypeInformers, shardName)
	delete(c.shardWorkspaceAuthConfigInformers, shardName)
}

func (c *Controller) Lookup(wsType index.WorkspaceType) (authenticator.Request, bool) {
	return c.authIndex.Lookup(wsType)
}
