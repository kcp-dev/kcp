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

// Package shardrepresentation runs on the shard hosting the root workspace
// and mirrors the cache server's view of Shard objects - the same source the
// Admin workspace serves - into the root workspace as read-only
// representations. This keeps `kubectl get shards` working in root while the
// authoritative objects stay shard-owned in the shard-local system:shard
// logical clusters. Admission continues to reject direct user writes in
// root; only system components (like this controller) may write.
package shardrepresentation

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	cacheshard "github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
)

const (
	ControllerName = "kcp-shard-representation"
	workKey        = "key"
)

// NewController returns a controller that maintains read-only Shard
// representations in the root workspace, mirrored from the cache server.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	globalShardInformer corev1alpha1informers.ShardClusterInformer,
	localShardInformer corev1alpha1informers.ShardClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		listGlobalShards: func() ([]*corev1alpha1.Shard, error) {
			return globalShardInformer.Lister().List(labels.Everything())
		},
		listRootShards: func() ([]*corev1alpha1.Shard, error) {
			return localShardInformer.Cluster(core.RootCluster).Lister().List(labels.Everything())
		},
		createShard: func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error) {
			return kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Create(ctx, shard, metav1.CreateOptions{})
		},
		updateShard: func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error) {
			return kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		},
		updateShardStatus: func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error) {
			return kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().UpdateStatus(ctx, shard, metav1.UpdateOptions{})
		},
		deleteShard: func(ctx context.Context, name string) error {
			return kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Delete(ctx, name, metav1.DeleteOptions{})
		},
	}

	enqueue := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { c.queue.Add(workKey) },
		UpdateFunc: func(_, _ interface{}) { c.queue.Add(workKey) },
		DeleteFunc: func(_ interface{}) { c.queue.Add(workKey) },
	}
	_, _ = globalShardInformer.Informer().AddEventHandler(events.WithoutSyncs(enqueue))
	_, _ = localShardInformer.Informer().AddEventHandler(enqueue)

	return c, nil
}

// Controller mirrors the cache server's Shard objects into the root
// workspace as representations.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	listGlobalShards  func() ([]*corev1alpha1.Shard, error)
	listRootShards    func() ([]*corev1alpha1.Shard, error)
	createShard       func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error)
	updateShard       func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error)
	updateShardStatus func(ctx context.Context, shard *corev1alpha1.Shard) (*corev1alpha1.Shard, error)
	deleteShard       func(ctx context.Context, name string) error
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	// reconcile once at startup even without events, so leftover
	// representations converge after e.g. an upgrade.
	c.queue.Add(workKey)

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

	if err := c.reconcile(ctx); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

// reconcile makes the root workspace's Shard objects match the deduplicated
// view of the cache server: one representation per shard name, sourced from
// the shard-owned authoritative copy (system:shard logical cluster) when
// present, and removed when the shard leaves the cache.
func (c *Controller) reconcile(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	global, err := c.listGlobalShards()
	if err != nil {
		return err
	}

	// one source object per shard name; the shard-owned copy wins over a
	// legacy copy in the root workspace (mixed-version windows).
	desired := map[string]*corev1alpha1.Shard{}
	for _, shard := range global {
		existing, seen := desired[shard.Name]
		if seen && logicalcluster.From(existing) == configshard.SystemShardCluster {
			continue
		}
		if !seen || logicalcluster.From(shard) == configshard.SystemShardCluster {
			desired[shard.Name] = shard
		}
	}

	current, err := c.listRootShards()
	if err != nil {
		return err
	}
	currentByName := map[string]*corev1alpha1.Shard{}
	for _, shard := range current {
		currentByName[shard.Name] = shard
	}

	var errs []error
	for name, source := range desired {
		representation := representationFor(source)
		existing, found := currentByName[name]
		if !found {
			logger.V(2).Info("creating Shard representation in the root workspace", "shard", name)
			created, err := c.createShard(ctx, representation)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				errs = append(errs, err)
				continue
			}
			if err == nil {
				if !equality.Semantic.DeepEqual(created.Status, representation.Status) {
					created.Status = representation.Status
					if _, err := c.updateShardStatus(ctx, created); err != nil {
						errs = append(errs, err)
					}
				}
			}
			continue
		}

		updated := existing.DeepCopy()
		updated.Labels = representation.Labels
		updated.Annotations = representation.Annotations
		// the storage layer stamps the logical cluster annotation on stored
		// objects; preserve it or every comparison sees a phantom diff and
		// the controller hot-loops.
		if v, ok := existing.Annotations[logicalcluster.AnnotationKey]; ok {
			if updated.Annotations == nil {
				updated.Annotations = map[string]string{}
			}
			updated.Annotations[logicalcluster.AnnotationKey] = v
		}
		updated.Spec = representation.Spec
		if !equality.Semantic.DeepEqual(updated.ObjectMeta, existing.ObjectMeta) || !equality.Semantic.DeepEqual(updated.Spec, existing.Spec) {
			logger.V(2).Info("updating Shard representation in the root workspace", "shard", name)
			var err error
			if updated, err = c.updateShard(ctx, updated); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if !equality.Semantic.DeepEqual(existing.Status, representation.Status) {
			updated.Status = representation.Status
			logger.V(2).Info("updating Shard representation status in the root workspace", "shard", name)
			if _, err := c.updateShardStatus(ctx, updated); err != nil {
				errs = append(errs, err)
			}
		}
	}

	for name := range currentByName {
		if _, ok := desired[name]; ok {
			continue
		}
		logger.V(2).Info("deleting Shard representation from the root workspace", "shard", name)
		if err := c.deleteShard(ctx, name); err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, err)
		}
	}

	return utilerrors.NewAggregate(errs)
}

// representationFor projects a cache-server Shard object onto the shape of
// its representation in the root workspace: same name, labels, spec and
// status; annotations minus the logical cluster and cache bookkeeping.
func representationFor(source *corev1alpha1.Shard) *corev1alpha1.Shard {
	representation := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:   source.Name,
			Labels: source.Labels,
		},
		Spec:   source.Spec,
		Status: source.Status,
	}
	for key, value := range source.Annotations {
		switch key {
		case logicalcluster.AnnotationKey, cacheshard.AnnotationKey,
			replication.AnnotationKeyOriginalResourceUID, replication.AnnotationKeyOriginalResourceVersion:
			continue
		}
		if representation.Annotations == nil {
			representation.Annotations = map[string]string{}
		}
		representation.Annotations[key] = value
	}
	return representation
}
