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
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
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
// For example: shards/{shardName}/clusters/{clusterName}/apis/apis.kcp.io/v1alpha1/apiexports.
func NewController(
	shardName string,
	dynamicCacheClient kcpdynamic.ClusterInterface,
	dynamicLocalClient kcpdynamic.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) (*controller, error) {
	c := &controller{
		shardName:                      shardName,
		queue:                          workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		dynamicCacheClient:             dynamicCacheClient,
		dynamicLocalClient:             dynamicLocalClient,
		localAPIExportLister:           localKcpInformers.Apis().V1alpha1().APIExports().Lister(),
		localAPIResourceSchemaLister:   localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
		localShardLister:               localKcpInformers.Core().V1alpha1().Shards().Lister(),
		localWorkspaceTypeLister:       localKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Lister(),
		globalAPIExportIndexer:         globalKcpInformers.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		globalAPIResourceSchemaIndexer: globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().GetIndexer(),
		globalShardIndexer:             globalKcpInformers.Core().V1alpha1().Shards().Informer().GetIndexer(),
		globalWorkspaceTypeIndexer:     globalKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(),
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
		globalKcpInformers.Core().V1alpha1().Shards().Informer().GetIndexer(),
		cache.Indexers{
			ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)

	indexers.AddIfNotPresentOrDie(
		globalKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(),
		cache.Indexers{
			ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
		},
	)

	localKcpInformers.Apis().V1alpha1().APIExports().Informer().AddEventHandler(c.objectInformerEventHandler(apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")))
	globalKcpInformers.Apis().V1alpha1().APIExports().Informer().AddEventHandler(c.objectInformerEventHandler(apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")))

	localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().AddEventHandler(c.objectInformerEventHandler(apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas")))
	globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer().AddEventHandler(c.objectInformerEventHandler(apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas")))

	localKcpInformers.Core().V1alpha1().Shards().Informer().AddEventHandler(c.objectInformerEventHandler(corev1alpha1.SchemeGroupVersion.WithResource("shards")))
	globalKcpInformers.Core().V1alpha1().Shards().Informer().AddEventHandler(c.objectInformerEventHandler(corev1alpha1.SchemeGroupVersion.WithResource("shards")))

	localKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().AddEventHandler(c.objectInformerEventHandler(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes")))
	globalKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer().AddEventHandler(c.objectInformerEventHandler(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes")))

	return c, nil
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

func (c *controller) objectInformerEventHandler(gvr schema.GroupVersionResource) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueObject(obj, gvr) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, gvr) },
		DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, gvr) },
	}
}

type controller struct {
	shardName string
	queue     workqueue.RateLimitingInterface

	dynamicCacheClient kcpdynamic.ClusterInterface
	dynamicLocalClient kcpdynamic.ClusterInterface

	localAPIExportLister         apisv1alpha1listers.APIExportClusterLister
	localAPIResourceSchemaLister apisv1alpha1listers.APIResourceSchemaClusterLister
	localShardLister             corev1alpha1listers.ShardClusterLister
	localWorkspaceTypeLister     tenancyv1alpha1listers.WorkspaceTypeClusterLister

	globalAPIExportIndexer         cache.Indexer
	globalAPIResourceSchemaIndexer cache.Indexer
	globalShardIndexer             cache.Indexer
	globalWorkspaceTypeIndexer     cache.Indexer
}
