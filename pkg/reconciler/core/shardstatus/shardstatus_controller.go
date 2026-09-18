/*
Copyright 2026 The kcp Authors.

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

package shardstatus

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-shard-status"

	// updateDelay debounces status updates: LogicalCluster events enqueue with this
	// delay, so bursts of workspace creations and deletions coalesce into a single
	// status write.
	updateDelay = 5 * time.Second

	// queueKey is the only key ever enqueued: the controller publishes status for
	// exactly one Shard, its own.
	queueKey = "publish"
)

// NewController returns a controller that publishes the number of LogicalClusters
// hosted on this shard into status.used of this shard's own, authoritative Shard
// object in the local system:shard logical cluster. Replication carries it to the
// cache server, which is where the workspace scheduler reads shards from.
// LogicalClusters only exist in the cache server if they are explicitly marked for
// replication, so the count has to be taken from the local informer of each shard
// and self-reported. It runs on every shard.
func NewController(
	shardName string,
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) *Controller {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		shardName: shardName,
		getShard: func(ctx context.Context) (*corev1alpha1.Shard, error) {
			return kcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1alpha1().Shards().Get(ctx, shardName, metav1.GetOptions{})
		},
		updateShardStatus: func(ctx context.Context, shard *corev1alpha1.Shard) error {
			_, err := kcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1alpha1().Shards().UpdateStatus(ctx, shard, metav1.UpdateOptions{})
			return err
		},
		countLogicalClusters: func() int64 {
			return int64(len(logicalClusterInformer.Informer().GetStore().ListKeys()))
		},
	}

	// Only adds and deletes can change the count; updates are ignored.
	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { c.enqueue() },
		DeleteFunc: func(obj any) { c.enqueue() },
	})

	return c
}

// Controller publishes the observed number of LogicalClusters on this shard into
// status.used of this shard's own Shard object in the local system:shard cluster.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	shardName            string
	getShard             func(ctx context.Context) (*corev1alpha1.Shard, error)
	updateShardStatus    func(ctx context.Context, shard *corev1alpha1.Shard) error
	countLogicalClusters func() int64
}

func (c *Controller) enqueue() {
	c.queue.AddAfter(queueKey, updateDelay)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	// publish an initial count even if no LogicalCluster events ever fire,
	// e.g. on a fresh shard without any workspaces.
	c.queue.Add(queueKey)

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	if err := c.process(ctx); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	count := c.countLogicalClusters()
	shard, err := c.getShard(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// the shard has not registered its own Shard object yet, try
			// again shortly.
			c.queue.AddAfter(queueKey, updateDelay)
			return nil
		}
		return err
	}

	if used, ok := shard.Status.Used[corev1alpha1.ResourceWorkspaces]; ok && used.Value() == count {
		return nil
	}

	if shard.Status.Used == nil {
		shard.Status.Used = corev1.ResourceList{}
	}
	shard.Status.Used[corev1alpha1.ResourceWorkspaces] = *resource.NewQuantity(count, resource.DecimalSI)
	logger.V(4).Info("updating Shard status.used", "shard", c.shardName, "workspaces", count)
	return c.updateShardStatus(ctx, shard)
}
