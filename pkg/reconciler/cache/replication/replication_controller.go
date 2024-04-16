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

package replication

import (
	"context"
	"fmt"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-replication-controller"
)

type ReplicatedGVR struct {
	Kind          string
	Filter        func(u *unstructured.Unstructured) bool
	Global, Local cache.SharedIndexInformer
}

// NewController returns a new replication controller.
//
// The replicated object will be placed under the same cluster as the original object.
// In addition to that, all replicated objects will be placed under the shard taken from the shardName argument.
// For example: shards/{shardName}/clusters/{clusterName}/apis/apis.kcp.io/v1alpha1/apiexports.
func NewController(
	shardName string,
	dynamicCacheClient kcpdynamic.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
	localKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	globalKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	gvrs map[schema.GroupVersionResource]ReplicatedGVR,
) (*controller, error) {
	c := &controller{
		shardName:          shardName,
		queue:              workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		dynamicCacheClient: dynamicCacheClient,

		gvrs: gvrs,
	}

	for gvr, info := range c.gvrs {
		// shadow gvr to get the right value in the closure
		gvr := gvr
		_, _ = info.Local.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: IsNoSystemClusterName,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { c.enqueueObject(obj, gvr) },
				UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, gvr) },
				DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, gvr) },
			},
		})

		_, _ = info.Global.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: IsNoSystemClusterName, // not really needed, but cannot harm
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { c.enqueueCacheObject(obj, gvr) },
				UpdateFunc: func(_, obj interface{}) { c.enqueueCacheObject(obj, gvr) },
				DeleteFunc: func(obj interface{}) { c.enqueueCacheObject(obj, gvr) },
			},
		})
	}

	return c, nil
}

func (c *controller) enqueueObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

func (c *controller) enqueueCacheObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, workers int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	grKey, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(grKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), grKey.(string))
	ctx = klog.NewContext(ctx, logger)
	err := c.reconcile(ctx, grKey.(string))
	if err == nil {
		c.queue.Forget(grKey)
		return true
	}

	runtime.HandleError(fmt.Errorf("%v failed with: %w", grKey, err))
	c.queue.AddRateLimited(grKey)

	return true
}

func IsNoSystemClusterName(obj interface{}) bool {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return false
	}

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	if strings.HasPrefix(clusterName.String(), "system:") {
		return false
	}
	return true
}

type controller struct {
	shardName string
	queue     workqueue.RateLimitingInterface

	dynamicCacheClient kcpdynamic.ClusterInterface

	gvrs map[schema.GroupVersionResource]ReplicatedGVR
}
