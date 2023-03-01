/*
Copyright 2023 The KCP Authors.

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

package upsync

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncerindexers "github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const cleanupControllerName = "kcp-resource-upsyncer-cleanup"

// NewUpSyncer returns a new controller which upsyncs, through the Upsyncer virtual workspace, downstream resources
// which are part of the upsyncable resource types (fixed limited list for now), and provide
// the following labels:
//   - internal.workload.kcp.io/cluster: <sync target key>
//   - state.workload.kcp.io/<sync target key>: Upsync
//
// and optionally, for cluster-wide resources, the `kcp.io/namespace-locator` annotation
// filled with the information necessary identify the upstream workspace to upsync to.
func NewUpSyncerCleanupController(syncerLogger logr.Logger, syncTargetClusterName logicalcluster.Name,
	syncTargetName string, syncTargetUID types.UID, syncTargetKey string,
	upstreamClusterClient kcpdynamic.ClusterInterface,
	ddsifForUpstreamUpsyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
) (*cleanupController, error) {
	c := &cleanupController{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), cleanupControllerName),
		cleanupReconciler: cleanupReconciler{
			getUpstreamClient: func(clusterName logicalcluster.Name) (dynamic.Interface, error) {
				return upstreamClusterClient.Cluster(clusterName.Path()), nil
			},
			getDownstreamLister: func(gvr schema.GroupVersionResource) (cache.GenericLister, error) {
				informers, notSynced := ddsifForDownstream.Informers()
				informer, ok := informers[gvr]
				if !ok {
					if shared.ContainsGVR(notSynced, gvr) {
						return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
					}
					return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
				}
				return informer.Lister(), nil
			},
			listDownstreamNamespacesByLocator: func(jsonLocator string) ([]*unstructured.Unstructured, error) {
				nsInformer, err := ddsifForDownstream.ForResource(namespaceGVR)
				if err != nil {
					return nil, err
				}
				return indexers.ByIndex[*unstructured.Unstructured](nsInformer.Informer().GetIndexer(), syncerindexers.ByNamespaceLocatorIndexName, jsonLocator)
			},

			syncTargetName:        syncTargetName,
			syncTargetClusterName: syncTargetClusterName,
			syncTargetUID:         syncTargetUID,
		},
	}
	logger := logging.WithReconciler(syncerLogger, controllerName)

	ddsifForUpstreamUpsyncer.AddEventHandler(ddsif.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			if gvr == namespaceGVR {
				return
			}
			c.enqueue(gvr, obj, logger)
		},
	})

	return c, nil
}

type cleanupController struct {
	queue workqueue.RateLimitingInterface

	cleanupReconciler
}

func (c *cleanupController) enqueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logging.WithQueueKey(logger, key).V(2).Info("queueing", "gvr", gvr.String())
	queueKey := queueKey{
		gvr: gvr,
		key: key,
	}

	c.queue.Add(queueKey)
}

func (c *cleanupController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting upsync workers")
	defer logger.Info("Stopping upsync workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (c *cleanupController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *cleanupController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(queueKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key.key).WithValues("gvr", key.gvr)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key.key, key.gvr); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q (%s), err: %w", controllerName, key.key, key.gvr.String(), err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *cleanupController) process(ctx context.Context, key string, gvr schema.GroupVersionResource) (bool, error) {
	logger := klog.FromContext(ctx)

	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}

	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NamespaceKey, namespace, logging.NameKey, name)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.cleanupReconciler.reconcile(ctx, gvr, clusterName, namespace, name)
	if err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}
