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

package clusterworkspaceshard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-clusterworkspaceshard"
)

func NewController(
	rootKcpClient kcpclientset.ClusterInterface,
	clusterWorkspaceShardInformer tenancyinformers.ClusterWorkspaceShardClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue:                        queue,
		kcpClient:                    rootKcpClient,
		clusterWorkspaceShardIndexer: clusterWorkspaceShardInformer.Informer().GetIndexer(),
		clusterWorkspaceShardLister:  clusterWorkspaceShardInformer.Lister(),
	}

	clusterWorkspaceShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

// Controller watches WorkspaceShards and Secrets in order to make sure every ClusterWorkspaceShard
// has its URL exposed when a valid kubeconfig is connected to it.
type Controller struct {
	queue workqueue.RateLimitingInterface

	kcpClient kcpclientset.ClusterInterface

	clusterWorkspaceShardIndexer cache.Indexer
	clusterWorkspaceShardLister  tenancyv1alpha1listers.ClusterWorkspaceShardClusterLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing ClusterWorkspaceShard")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
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
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}
	if namespace != "" {
		logger.Error(errors.New("namespace found in key for cluster-wide ClusterWorkspaceShard object"), "invalid key")
		return nil
	}

	obj, err := c.clusterWorkspaceShardLister.Cluster(clusterName).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	previous := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, obj.Status) {
		oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspaceShard{
			Status: previous.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for workspace shard %s|%s/%s: %w", tenancyv1alpha1.RootCluster, namespace, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				UID:             previous.UID,
				ResourceVersion: previous.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for workspace shard %s|%s/%s: %w", tenancyv1alpha1.RootCluster, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for workspace shard %s|%s/%s: %w", tenancyv1alpha1.RootCluster, namespace, name, err)
		}
		_, uerr := c.kcpClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaceShards().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	logger.V(6).Info("processed ClusterWorkspaceShard")
	return nil
}

func (c *Controller) reconcile(ctx context.Context, workspaceShard *tenancyv1alpha1.ClusterWorkspaceShard) error {
	return nil
}
