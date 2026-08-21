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

// Package shardmirror mirrors shard-owned Shard objects (living in each
// shard's local system:shard logical cluster and replicated to the cache
// server) into the root workspace as read-only representations. It runs on the
// root shard only. Representations are marked with the
// core.kcp.io/shard-representation annotation, continuously overwritten from
// the authoritative object, and pruned when the authoritative object
// disappears. Shard objects in the root workspace without that annotation
// (e.g. created by tests or old shards) are left alone.
package shardmirror

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

const (
	ControllerName = "kcp-shard-mirror"
)

// NewController returns a controller mirroring authoritative Shard objects
// from the cache server into the root workspace as read-only representations.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	localShardInformer corev1alpha1informers.ShardClusterInformer,
	cacheShardInformer corev1alpha1informers.ShardClusterInformer,
) *Controller {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getSourceShard: func(name string) (*corev1alpha1.Shard, error) {
			return cacheShardInformer.Cluster(configshard.SystemShardCluster).Lister().Get(name)
		},
		getLocalShard: func(name string) (*corev1alpha1.Shard, error) {
			return localShardInformer.Cluster(core.RootCluster).Lister().Get(name)
		},
		createShard: func(ctx context.Context, shard *corev1alpha1.Shard) error {
			_, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Create(ctx, shard, metav1.CreateOptions{})
			return err
		},
		updateShard: func(ctx context.Context, shard *corev1alpha1.Shard) error {
			_, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
			return err
		},
		updateShardStatus: func(ctx context.Context, shard *corev1alpha1.Shard) error {
			_, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().UpdateStatus(ctx, shard, metav1.UpdateOptions{})
			return err
		},
		deleteShard: func(ctx context.Context, name string) error {
			return kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	_, _ = cacheShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})
	_, _ = localShardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	return c
}

// Controller mirrors authoritative Shard objects into the root workspace.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getSourceShard func(name string) (*corev1alpha1.Shard, error)
	getLocalShard  func(name string) (*corev1alpha1.Shard, error)

	createShard       func(ctx context.Context, shard *corev1alpha1.Shard) error
	updateShard       func(ctx context.Context, shard *corev1alpha1.Shard) error
	updateShardStatus func(ctx context.Context, shard *corev1alpha1.Shard) error
	deleteShard       func(ctx context.Context, name string) error
}

// enqueue maps any Shard event (authoritative copy in the cache, or local copy
// in the root workspace) to the shard name.
func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	// only authoritative copies and root-workspace representations are of interest.
	if clusterName != configshard.SystemShardCluster && clusterName != core.RootCluster {
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), name)
	logger.V(4).Info("queueing Shard")
	c.queue.Add(name)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

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
	name, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(name)

	if err := c.reconcile(ctx, name); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, name, err))
		c.queue.AddRateLimited(name)
		return true
	}
	c.queue.Forget(name)
	return true
}

func (c *Controller) reconcile(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	source, err := c.getSourceShard(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	sourceExists := !apierrors.IsNotFound(err)

	local, err := c.getLocalShard(name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	localExists := !apierrors.IsNotFound(err)

	if !sourceExists {
		// prune representations whose authoritative object is gone. Leave
		// shard objects not managed by this controller alone.
		if localExists && isRepresentation(local) {
			logger.V(2).Info("deleting Shard representation, authoritative object is gone", "shard", name)
			if err := c.deleteShard(ctx, name); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	}

	desired := representationFor(source)

	if !localExists {
		logger.V(2).Info("creating Shard representation", "shard", name)
		if err := c.createShard(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}

	updated := local.DeepCopy()
	updated.Labels = desired.Labels
	updated.Annotations = desired.Annotations
	// the logical cluster annotation is stamped onto stored objects by the
	// server; keep it so the comparison below converges instead of updating
	// on every resync.
	if cluster, ok := local.Annotations[logicalcluster.AnnotationKey]; ok {
		updated.Annotations[logicalcluster.AnnotationKey] = cluster
	}
	updated.Spec = desired.Spec
	if !equality.Semantic.DeepEqual(local.Labels, updated.Labels) ||
		!equality.Semantic.DeepEqual(local.Annotations, updated.Annotations) ||
		!equality.Semantic.DeepEqual(local.Spec, updated.Spec) {
		logger.V(2).Info("updating Shard representation", "shard", name)
		// status is synced on the next reconcile triggered by the update
		return c.updateShard(ctx, updated)
	}

	if !equality.Semantic.DeepEqual(local.Status, source.Status) {
		updated.Status = source.Status
		logger.V(2).Info("updating Shard representation status", "shard", name)
		return c.updateShardStatus(ctx, updated)
	}

	return nil
}

func isRepresentation(shard *corev1alpha1.Shard) bool {
	return shard.Annotations[corev1alpha1.ShardRepresentationAnnotationKey] != ""
}

// representationFor builds the root-workspace representation of an
// authoritative Shard object from the cache server, stripping cache-server
// bookkeeping annotations.
func representationFor(source *corev1alpha1.Shard) *corev1alpha1.Shard {
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        source.Name,
			Labels:      source.Labels,
			Annotations: map[string]string{},
		},
		Spec:   source.Spec,
		Status: source.Status,
	}
	for k, v := range source.Annotations {
		shard.Annotations[k] = v
	}
	delete(shard.Annotations, logicalcluster.AnnotationKey)
	delete(shard.Annotations, genericrequest.ShardAnnotationKey)
	delete(shard.Annotations, replication.AnnotationKeyOriginalResourceVersion)
	delete(shard.Annotations, replication.AnnotationKeyOriginalResourceUID)
	shard.Annotations[corev1alpha1.ShardRepresentationAnnotationKey] = "true"
	return shard
}
