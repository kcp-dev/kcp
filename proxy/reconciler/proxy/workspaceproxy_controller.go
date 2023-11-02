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

package workspaceproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxyclientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/cluster"
	proxyv1alpha1informers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions/proxy/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const ControllerName = "proxy-workspace-proxy-controller"

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	proxyClusterClient proxyclientset.ClusterInterface,
	workspaceProxyInformer proxyv1alpha1informers.WorkspaceProxyClusterInformer,
	workspaceShardInformer corev1alpha1informers.ShardClusterInformer,
	globalWorkspaceShardInformer corev1alpha1informers.ShardClusterInformer,
) *Controller {
	c := &Controller{
		queue:                 workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		kcpClusterClient:      kcpClusterClient,
		proxyClusterClient:    proxyClusterClient,
		workspaceProxyIndexer: workspaceProxyInformer.Informer().GetIndexer(),
		listWorkspaceShards:   informer.NewListerWithFallback[*corev1alpha1.Shard](workspaceShardInformer.Lister(), globalWorkspaceShardInformer.Lister()),
	}

	// Watch for events related to WorkspaceProxy
	_, _ = workspaceProxyInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspaceProxy(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceProxy(obj) },
		DeleteFunc: func(obj interface{}) {},
	})

	// Watch for events related to workspaceShards
	_, _ = workspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueWorkspaceShard(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueWorkspaceShard(obj) },
	})

	return c
}

type Controller struct {
	queue                 workqueue.RateLimitingInterface
	kcpClusterClient      kcpclientset.ClusterInterface
	proxyClusterClient    proxyclientset.ClusterInterface
	listWorkspaceShards   informer.FallbackListFunc[*corev1alpha1.Shard]
	workspaceProxyIndexer cache.Indexer
}

func (c *Controller) enqueueWorkspaceProxy(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing WorkspaceProxy")
	c.queue.Add(key)
}

// On workspaceShard changes, enqueue all the workspaceProxy.
func (c *Controller) enqueueWorkspaceShard(obj interface{}) {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj.(*corev1alpha1.Shard))
	for _, syncTarget := range c.workspaceProxyIndexer.List() {
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(syncTarget)
		if err != nil {
			runtime.HandleError(err)
			return
		}
		logging.WithQueueKey(logger, key).V(2).Info("queueing WorkspaceProxy because of Shard")
		c.queue.Add(key)
	}
}

// Start starts the controller workers.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
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
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("failed to sync %q: %w", key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	obj, exists, err := c.workspaceProxyIndexer.GetByKey(key)
	if err != nil {
		logger.Error(err, "failed to get WorkspaceProxy")
		return nil
	}

	if !exists {
		logger.Info("workspaceProxy was deleted")
		return nil
	}

	currentWorkspaceProxy := obj.(*proxyv1alpha1.WorkspaceProxy)
	logger = logging.WithObject(klog.FromContext(ctx), currentWorkspaceProxy)
	ctx = klog.NewContext(ctx, logger)

	workspacesShards, err := c.listWorkspaceShards(labels.Everything())
	if err != nil {
		return err
	}

	newWorkspaceProxy, err := c.reconcile(ctx, currentWorkspaceProxy, workspacesShards)
	if err != nil {
		logger.Error(err, "failed to reconcile workspaceProxy")
		return err
	}

	if reflect.DeepEqual(currentWorkspaceProxy, newWorkspaceProxy) {
		return nil
	}

	currentWorkspaceProxyJSON, err := json.Marshal(currentWorkspaceProxy)
	if err != nil {
		logger.Error(err, "failed to marshal workspaceProxy")
		return err
	}
	newWorkspaceProxyJSON, err := json.Marshal(newWorkspaceProxy)
	if err != nil {
		logger.Error(err, "failed to marshal workspaceProxy")
		return err
	}

	patchBytes, err := jsonpatch.CreateMergePatch(currentWorkspaceProxyJSON, newWorkspaceProxyJSON)
	if err != nil {
		logger.Error(err, "failed to create merge patch for workspaceProxy")
		return err
	}

	if !reflect.DeepEqual(currentWorkspaceProxy.ObjectMeta, newWorkspaceProxy.ObjectMeta) || !reflect.DeepEqual(currentWorkspaceProxy.Spec, newWorkspaceProxy.Spec) {
		logger.WithValues("patch", string(patchBytes)).V(2).Info("patching workspaceProxy")
		if _, err := c.proxyClusterClient.Cluster(logicalcluster.From(currentWorkspaceProxy).Path()).ProxyV1alpha1().WorkspaceProxies().Patch(ctx, currentWorkspaceProxy.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			logger.Error(err, "failed to patch workspaceProxy")
			return err
		}
	}

	if !reflect.DeepEqual(currentWorkspaceProxy.Status, newWorkspaceProxy.Status) {
		logger.WithValues("patch", string(patchBytes)).V(2).Info("patching workspaceProxy status")
		if _, err := c.proxyClusterClient.Cluster(logicalcluster.From(currentWorkspaceProxy).Path()).ProxyV1alpha1().WorkspaceProxies().Patch(ctx, currentWorkspaceProxy.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status"); err != nil {
			logger.Error(err, "failed to patch workspaceProxy status")
			return err
		}
	}

	return nil
}
