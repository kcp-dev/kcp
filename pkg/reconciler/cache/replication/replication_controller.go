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
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-replication-controller"
)

// NewController returns a new replication controller.
//
// The replicated object will be placed under the same cluster as the original object.
// In addition to that, all replicated objects will be placed under the shard taken from the shardName argument.
// For example: shards/{shardName}/clusters/{clusterName}/apis/apis.kcp.dev/v1alpha1/apiexports
func NewController(
	shardName string,
	dynamicCacheClient kcpdynamic.ClusterInterface,
	dynamicLocalClient kcpdynamic.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) (*controller, error) {
	c := &controller{
		shardName:                          shardName,
		queue:                              workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		dynamicCacheClient:                 dynamicCacheClient,
		dynamicLocalClient:                 dynamicLocalClient,
		localAPIExportLister:               localKcpInformers.Apis().V1alpha1().APIExports().Lister(),
		localAPIResourceSchemaLister:       localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
		localClusterWorkspaceShardLister:   localKcpInformers.Tenancy().V1alpha1().ClusterWorkspaceShards().Lister(),
		globalAPIExportIndexer:             globalKcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		globalAPIResourceSchemaIndexer:     globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().GetIndexer(),
		globalClusterWorkspaceShardIndexer: globalKcpInformers.Tenancy().V1alpha1().ClusterWorkspaceShards().Informer().GetIndexer(),
	}

	indexers.AddIfNotPresentOrDie(
		globalKcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		cache.Indexers{
			ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)

	indexers.AddIfNotPresentOrDie(
		globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().GetIndexer(),
		cache.Indexers{
			ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)

	indexers.AddIfNotPresentOrDie(
		globalKcpInformers.Tenancy().V1alpha1().ClusterWorkspaceShards().Informer().GetIndexer(),
		cache.Indexers{
			ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)

	localKcpInformers.Apis().V1alpha1().APIExports().Informer().AddEventHandler(c.apiExportInformerEventHandler())
	localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().AddEventHandler(c.apiResourceSchemaInformerEventHandler())
	localKcpInformers.Tenancy().V1alpha1().ClusterWorkspaceShards().Informer().AddEventHandler(c.clusterWorkspaceShardInformerEventHandler())
	globalKcpInformers.Apis().V1alpha1().APIExports().Informer().AddEventHandler(c.apiExportInformerEventHandler())
	globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().AddEventHandler(c.apiResourceSchemaInformerEventHandler())
	globalKcpInformers.Tenancy().V1alpha1().ClusterWorkspaceShards().Informer().AddEventHandler(c.clusterWorkspaceShardInformerEventHandler())

	return c, nil
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	c.enqueueObject(obj, apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"))
}

func (c *controller) enqueueAPIResourceSchema(obj interface{}) {
	c.enqueueObject(obj, apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"))
}

func (c *controller) enqueueClusterWorkspaceShard(obj interface{}) {
	c.enqueueObject(obj, tenancyv1alpha1.SchemeGroupVersion.WithResource("clusterworkspaceshards"))
}

func (c *controller) enqueueObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%v::%v", gvr.String(), key)
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

func (c *controller) apiExportInformerEventHandler() cache.ResourceEventHandler {
	return objectInformerEventHandler(c.enqueueAPIExport)
}

func (c *controller) apiResourceSchemaInformerEventHandler() cache.ResourceEventHandler {
	return objectInformerEventHandler(c.enqueueAPIResourceSchema)
}

func (c *controller) clusterWorkspaceShardInformerEventHandler() cache.ResourceEventHandler {
	return objectInformerEventHandler(c.enqueueClusterWorkspaceShard)
}

func objectInformerEventHandler(enqueueObject func(obj interface{})) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { enqueueObject(obj) },
		UpdateFunc: func(_, obj interface{}) { enqueueObject(obj) },
		DeleteFunc: func(obj interface{}) { enqueueObject(obj) },
	}
}

type controller struct {
	shardName string
	queue     workqueue.RateLimitingInterface

	dynamicCacheClient kcpdynamic.ClusterInterface
	dynamicLocalClient kcpdynamic.ClusterInterface

	localAPIExportLister             apisv1alpha1listers.APIExportClusterLister
	localAPIResourceSchemaLister     apisv1alpha1listers.APIResourceSchemaClusterLister
	localClusterWorkspaceShardLister tenancyv1alpha1listers.ClusterWorkspaceShardClusterLister

	globalAPIExportIndexer             cache.Indexer
	globalAPIResourceSchemaIndexer     cache.Indexer
	globalClusterWorkspaceShardIndexer cache.Indexer
}
