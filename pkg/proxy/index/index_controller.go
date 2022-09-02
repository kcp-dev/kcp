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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	controllerName = "kcp-clusterworkspace-index"

	clusterWorkspaceResyncPeriod = 2 * time.Hour
)

// Index implements a mapping from logical cluster to (shard) URL.
type Index interface {
	Lookup(logicalCluster logicalcluster.Name) (string, bool)
}

type ClusterWorkspaceClientGetter func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclient.Interface, error)

func NewController(
	rootHost string,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardInformer,
	clientGetter ClusterWorkspaceClientGetter,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &Controller{
		queue: queue,

		rootHost:     rootHost,
		clientGetter: clientGetter,

		clusterWorkspaceShardIndexer: clusterWorkspaceShardInformer.Informer().GetIndexer(),
		clusterWorkspaceShardLister:  clusterWorkspaceShardInformer.Lister(),

		shardClusterWorkspaceInformers: map[string]cache.SharedIndexInformer{},
		shardClusterWorkspaceStopCh:    map[string]chan struct{}{},

		workspaceShardNames: map[logicalcluster.Name]string{},
		shardBaseURLs:       map[string]string{},
	}

	c.clusterWorkspaceHandler = cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws := obj.(*tenancyv1alpha1.ClusterWorkspace)
			c.lock.RLock()
			got := c.workspaceShardNames[logicalcluster.From(ws).Join(ws.Name)]
			c.lock.RUnlock()

			if expected := ws.Status.Location.Current; got != expected {
				c.lock.Lock()
				defer c.lock.Unlock()
				c.workspaceShardNames[logicalcluster.From(ws).Join(ws.Name)] = expected
			}
		},
		UpdateFunc: func(old, obj interface{}) {
			ws := obj.(*tenancyv1alpha1.ClusterWorkspace)

			c.lock.RLock()
			got := c.workspaceShardNames[logicalcluster.From(ws).Join(ws.Name)]
			c.lock.RUnlock()

			if expected := ws.Status.Location.Current; got != expected {
				c.lock.Lock()
				defer c.lock.Unlock()
				c.workspaceShardNames[logicalcluster.From(ws).Join(ws.Name)] = expected
			}
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			ws := obj.(*tenancyv1alpha1.ClusterWorkspace)

			c.lock.Lock()
			defer c.lock.Unlock()
			delete(c.workspaceShardNames, logicalcluster.From(ws).Join(ws.Name))
		},
	}

	clusterWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
			c.lock.RLock()
			got := c.shardBaseURLs[shard.Name]
			c.lock.RUnlock()

			if expected := shard.Spec.BaseURL; got != expected {
				c.lock.Lock()
				defer c.lock.Unlock()
				c.shardBaseURLs[shard.Name] = expected
			}

			c.enqueueShard(shard)
		},
		UpdateFunc: func(old, obj interface{}) {
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)
			c.lock.RLock()
			got := c.shardBaseURLs[shard.Name]
			c.lock.RUnlock()

			if expected := shard.Spec.BaseURL; got != expected {
				c.lock.Lock()
				defer c.lock.Unlock()
				c.shardBaseURLs[shard.Name] = expected
			}

			// don't updates. Not of interest.
		},
		DeleteFunc: func(obj interface{}) {
			if final, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = final.Obj
			}
			shard := obj.(*tenancyv1alpha1.ClusterWorkspaceShard)

			c.lock.Lock()
			defer c.lock.Unlock()
			delete(c.shardBaseURLs, shard.Name)

			c.enqueueShard(shard)
		},
	})

	return c
}

// Controller watches ClusterWorkspaceShards on the root shard, and then starts informers
// for every ClusterWorkspaceShard, watching the ClusterWorkspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type Controller struct {
	queue workqueue.RateLimitingInterface

	rootHost     string
	clientGetter ClusterWorkspaceClientGetter

	clusterWorkspaceShardIndexer cache.Indexer
	clusterWorkspaceShardLister  tenancylisters.ClusterWorkspaceShardLister

	clusterWorkspaceHandler cache.ResourceEventHandler

	shardInformersLock             sync.RWMutex
	shardClusterWorkspaceInformers map[string]cache.SharedIndexInformer
	shardClusterWorkspaceStopCh    map[string]chan struct{}

	lock                sync.RWMutex
	workspaceShardNames map[logicalcluster.Name]string
	shardBaseURLs       map[string]string
}

// Start the controller. It does not really do anything, but to keep the shape of a normal
// controller, we keep it.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer func() {
		c.shardInformersLock.Lock()
		defer c.shardInformersLock.Unlock()
		for _, stopCh := range c.shardClusterWorkspaceStopCh {
			close(stopCh)
		}
	}()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting %s controller", controllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) enqueueShard(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Enqueueing ClusterWorkspaceShard %q", key)

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
	shard, err := c.clusterWorkspaceShardLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			c.shardInformersLock.Lock()
			defer c.shardInformersLock.Unlock()

			delete(c.shardClusterWorkspaceInformers, shard.Name)
			delete(c.shardClusterWorkspaceStopCh, shard.Name)

			return nil
		}
		return err
	}

	c.shardInformersLock.Lock()
	defer c.shardInformersLock.Unlock()

	if _, found := c.shardClusterWorkspaceInformers[shard.Name]; !found {
		client, err := c.clientGetter(shard)
		if err != nil {
			return err
		}
		informer := tenancyinformers.NewClusterWorkspaceInformer(client, clusterWorkspaceResyncPeriod, nil)
		informer.AddEventHandler(c.clusterWorkspaceHandler)

		stopCh := make(chan struct{})
		c.shardClusterWorkspaceInformers[shard.Name] = informer
		c.shardClusterWorkspaceStopCh[shard.Name] = stopCh

		go informer.Run(stopCh)

		// no need to wait. We only care about events and they arrive when they arrive.
	}

	return nil
}

func (c *Controller) Lookup(logicalCluster logicalcluster.Name) (string, bool) {
	if logicalCluster == tenancyv1alpha1.RootCluster {
		return c.rootHost, true
	}

	c.lock.RLock()
	defer c.lock.RUnlock()

	shardName, found := c.workspaceShardNames[logicalCluster]
	if !found {
		return "", false
	}
	url, found := c.shardBaseURLs[shardName]
	return url, found
}
