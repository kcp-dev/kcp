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

package replication

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// Locally we store object with the original form of the object.
// In the cache we store object with the CachedResource as a wrapper, and store the original GVR in the labels.
// This way we can extract the original GVR from the CachedResource and use it to enqueue the object.

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-cached-resource-replication-controller"
)

func getClusterNameFromObj(obj any) logicalcluster.Name {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return ""
	}

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return ""
	}

	return clusterName
}

// NewController returns a new replication controller.
func NewController(
	shardName string,
	localDynamicClusterClient kcpdynamic.ClusterInterface,
	globalDynamicClusterClient kcpdynamic.ClusterInterface,
	kcpCacheClient kcpclientset.ClusterInterface,
	cacheApiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface,
	cluster logicalcluster.Name,
	gvr schema.GroupVersionResource,
	replicated *ReplicatedGVR,
	requeueSelf func(),
	localLabelSelector labels.Selector,
) (*Controller, error) {
	c := &Controller{
		shardName: shardName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		localDynamicClusterClient:       localDynamicClusterClient,
		globalDynamicClusterClient:      globalDynamicClusterClient,
		cacheApiExtensionsClusterClient: cacheApiExtensionsClusterClient,
		replicated:                      replicated,
		requeueSelf:                     requeueSelf,
		onShutdownFuncs:                 make([]func(), 0),
		localLabelSelector:              localLabelSelector,
	}

	localHandler, err := c.replicated.Local.AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			return getClusterNameFromObj(obj) == cluster
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueObject(obj, gvr, "local") },
			UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, gvr, "local") },
			DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, gvr, "local") },
		},
	})
	if err != nil {
		return nil, err
	}
	c.onShutdownFuncs = append(c.onShutdownFuncs, func() {
		_ = c.replicated.Local.RemoveEventHandler(localHandler)
	})

	globalHandler, err := c.replicated.Global.AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			return getClusterNameFromObj(obj) == cluster
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueObject(obj, gvr, "global") },
			UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, gvr, "global") },
			DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, gvr, "global") },
		},
	})
	if err != nil {
		return nil, err
	}
	c.onShutdownFuncs = append(c.onShutdownFuncs, func() {
		_ = c.replicated.Global.RemoveEventHandler(globalHandler)
	})

	return c, nil
}

func (c *Controller) enqueueObject(obj interface{}, gvr schema.GroupVersionResource, source string) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.Info("queuing object", "gvrKey", gvrKey, "from", source)

	c.queue.Add(gvrKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, workers int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer func() {
		for _, cleanupFunc := range c.onShutdownFuncs {
			cleanupFunc()
		}
	}()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range workers {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	c.started = true
	<-ctx.Done()
	c.started = false
}

func (c *Controller) Started() bool {
	return c.started
}

func (c *Controller) SetLabelSelector(localLabelSelector labels.Selector) {
	c.localLabelSelector = localLabelSelector
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	gvrKey, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(gvrKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), gvrKey)
	ctx = klog.NewContext(ctx, logger)
	err := c.reconcile(ctx, gvrKey)
	if err == nil {
		c.queue.Forget(gvrKey)
		return true
	}

	utilruntime.HandleError(fmt.Errorf("%v failed with: %w", gvrKey, err))
	c.queue.AddRateLimited(gvrKey)

	return true
}

func (c *Controller) SetDeleted(ctx context.Context) {
	c.deleted = true
}

type Controller struct {
	shardName string
	queue     workqueue.TypedRateLimitingInterface[string]

	localDynamicClusterClient       kcpdynamic.ClusterInterface
	globalDynamicClusterClient      kcpdynamic.ClusterInterface
	cacheApiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface

	replicated *ReplicatedGVR

	// requeueSelf is called when we want to trigger parent object reconciliation.
	// Cache state is being managed by child controller, so we need to trigger parent object reconciliation
	// to update parent object status.
	// Example:
	// 1. Add new object to replicate, so GVR controller picks it up.
	// 2. Object is replicated to cache by child controller.
	// 3. requeueSelf is called to update parent object status.
	requeueSelf func()
	// onShutdownFuncs are cleanup functions that are called when the controller is stopped.
	onShutdownFuncs []func()

	// localLabelSelector is the label selector that we use to filter the objects that we want to replicate.
	// It is set when the controller is created and can be changed by the parent controller.
	localLabelSelector labels.Selector

	started bool
	deleted bool
}

type ReplicatedGVR struct {
	Kind          string
	Identity      string
	Filter        func(u *unstructured.Unstructured) bool
	Global, Local cache.SharedIndexInformer
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(replicated *ReplicatedGVR) {
	indexers.AddIfNotPresentOrDie(
		replicated.Global.GetIndexer(),
		cache.Indexers{
			byShardAndLogicalClusterAndNamespaceAndName: indexByShardAndLogicalClusterAndNamespaceAndName,
		},
	)
}

const (
	// byShardAndLogicalClusterAndNamespaceAndName is the name for the index that indexes by an object's shard and logical cluster, namespace and name.
	byShardAndLogicalClusterAndNamespaceAndName = "kcp-byShardAndLogicalClusterAndNamespaceAndName"
)

// indexByShardAndLogicalClusterAndNamespaceAndName is an index function that indexes by an object's shard and logical cluster, namespace and name.
func indexByShardAndLogicalClusterAndNamespaceAndName(obj interface{}) ([]string, error) {
	a, err := meta.Accessor(obj)
	if err != nil {
		return nil, err
	}
	annotations := a.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	shardName := annotations[genericapirequest.ShardAnnotationKey]

	key := shardAndLogicalClusterAndNamespaceKey(shardName, logicalcluster.From(a), a.GetNamespace(), a.GetName())

	return []string{key}, nil
}

// shardAndLogicalClusterAndNamespaceKey creates an index key from the given parameters.
// As of today this function is used by IndexByShardAndLogicalClusterAndNamespace indexer.
func shardAndLogicalClusterAndNamespaceKey(shard string, cluster logicalcluster.Name, namespace, name string) string {
	var key string
	if len(shard) > 0 {
		key += shard + "|"
	}
	if !cluster.Empty() {
		key += cluster.String() + "|"
	}
	if len(namespace) > 0 {
		key += namespace + "/"
	}
	if len(key) == 0 {
		return name
	}
	return fmt.Sprintf("%s%s", key, name)
}
