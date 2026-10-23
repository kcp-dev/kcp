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

// Package apiidentity maintains the APIIdentity registry on the cache server.
//
// Every identity has one registry entry, created by the APIExport controller
// of the shard whose export claimed it first. This controller runs on every
// shard and decides from the APIExports replicated to the cache server,
// which is the installation-wide view: the shard hosting the owner keeps the
// derived status current; an owner missing from the cache for longer than
// the grace period is gone, and the shard hosting the oldest remaining
// holder takes the identity over, or the identity is orphaned when no holder
// is left.
package apiidentity

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	adminv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/admin/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

const (
	ControllerName = "kcp-apiidentity"

	// MissingGracePeriod is how long an export may be missing from the cache
	// server before it is taken as gone. It covers the window between an
	// entry being written and the export's replicated copy arriving, and a
	// copy disappearing for a moment, as during a workspace migration.
	MissingGracePeriod = 30 * time.Second
)

// NewController creates the registry controller of this shard.
func NewController(
	cacheKcpClusterClient kcpclientset.ClusterInterface,
	apiIdentityInformer adminv1alpha1informers.APIIdentityClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	isCore func(*apisv1alpha2.APIExport) bool,
) (*Controller, error) {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
	)
	c := &Controller{
		queue:    queue,
		registry: registryScoped{cacheKcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().APIIdentities()},
		getAPIIdentity: func(name string) (*adminv1alpha1.APIIdentity, error) {
			return apiIdentityInformer.Lister().Cluster(configshard.SystemShardCluster).Get(name)
		},
		getLocalAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			return apiExportInformer.Lister().Cluster(clusterName).Get(name)
		},
		getGlobalAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			return globalAPIExportInformer.Lister().Cluster(clusterName).Get(name)
		},
		requeueAfter: queue.AddAfter,
		isCore:       isCore,
	}

	_, _ = apiIdentityInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj.(*adminv1alpha1.APIIdentity).Name) },
		UpdateFunc: func(_, newObj interface{}) { c.enqueue(newObj.(*adminv1alpha1.APIIdentity).Name) },
	}))

	// local exports: the owner's shard maintains the entry.
	_, _ = apiExportInformer.Informer().AddEventHandler(events.WithoutSyncs(exportHandler(c)))

	// replicated exports: the installation-wide view every decision is made
	// from. An export appearing in or vanishing from the cache changes what
	// the entry should say.
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(events.WithoutSyncs(exportHandler(c)))

	return c, nil
}

func exportHandler(c *Controller) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueExport(obj.(*apisv1alpha2.APIExport)) },
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldExport, newExport := oldObj.(*apisv1alpha2.APIExport), newObj.(*apisv1alpha2.APIExport)
			if oldExport.Status.IdentityHash != newExport.Status.IdentityHash {
				c.enqueueExport(oldExport)
			}
			c.enqueueExport(newExport)
		},
		DeleteFunc: func(obj interface{}) { c.enqueueExport(tombstone.Obj[*apisv1alpha2.APIExport](obj)) },
	}
}

// Controller maintains the registry entries from the cache server's view of
// the APIExports.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	registry           adminv1alpha1client
	getAPIIdentity     func(name string) (*adminv1alpha1.APIIdentity, error)
	getLocalAPIExport  func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	getGlobalAPIExport func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error)
	requeueAfter       func(identity string, delay time.Duration)
	isCore             func(*apisv1alpha2.APIExport) bool
}

func (c *Controller) enqueue(identity string) {
	if identity == "" {
		return
	}
	c.queue.Add(identity)
}

func (c *Controller) enqueueExport(export *apisv1alpha2.APIExport) {
	c.enqueue(export.Status.IdentityHash)
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
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	if err := c.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to reconcile identity %q: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) reconcile(ctx context.Context, identity string) error {
	entry, err := c.getAPIIdentity(identity)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	owner, err := c.getGlobalAPIExport(logicalcluster.Name(entry.Status.Owner.Cluster), entry.Status.Owner.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		return c.maintain(ctx, entry, owner)
	}
	return c.ownerMissing(ctx, entry)
}

// maintain keeps the derived status of an entry whose owner is in the cache.
// Any shard clears a stale OwnerMissing; the shard hosting the owner writes
// the rest.
func (c *Controller) maintain(ctx context.Context, entry *adminv1alpha1.APIIdentity, owner *apisv1alpha2.APIExport) error {
	_, err := c.getLocalAPIExport(logicalcluster.From(owner), owner.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	hostsOwner := err == nil

	return c.updateStatus(ctx, entry.Name, func(entry *adminv1alpha1.APIIdentity) {
		conditions.Delete(entry, adminv1alpha1.APIIdentityOwnerMissing)
		if !hostsOwner {
			return
		}
		entry.Status.Core = c.isCore(owner)
		// an owner that succeeded from holder is not a holder any more
		dropHolders(entry, append(c.goneHolders(entry), ref(owner)))
		conditions.Delete(entry, adminv1alpha1.APIIdentityOrphaned)
	})
}

// ownerMissing handles an entry whose owner is not in the cache. The first
// look records when it went missing; after the grace period the owner is
// gone and the identity passes to the oldest holder still in the cache, or
// is orphaned.
func (c *Controller) ownerMissing(ctx context.Context, entry *adminv1alpha1.APIIdentity) error {
	logger := klog.FromContext(ctx)

	if conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned) {
		// nobody serves it; whoever has the key reclaims it
		return nil
	}

	missing := conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing)
	if missing == nil || missing.Status != corev1.ConditionTrue {
		logger.V(2).Info("owner of identity is not in the cache, waiting for the grace period", "owner", entry.Status.Owner.Cluster+"|"+entry.Status.Owner.Name)
		if err := c.updateStatus(ctx, entry.Name, func(entry *adminv1alpha1.APIIdentity) {
			conditions.MarkTrue(entry, adminv1alpha1.APIIdentityOwnerMissing)
		}); err != nil {
			return err
		}
		c.requeueAfter(entry.Name, MissingGracePeriod)
		return nil
	}
	if remaining := MissingGracePeriod - time.Since(missing.LastTransitionTime.Time); remaining > 0 {
		c.requeueAfter(entry.Name, remaining)
		return nil
	}

	// the owner is gone
	successor := c.oldestHolderInCache(entry)
	if successor == nil {
		logger.Info("identity has no owner and no holder left, marking it orphaned")
		return c.updateStatus(ctx, entry.Name, func(entry *adminv1alpha1.APIIdentity) {
			dropHolders(entry, holderRefs(entry))
			conditions.Delete(entry, adminv1alpha1.APIIdentityOwnerMissing)
			conditions.MarkTrue(entry, adminv1alpha1.APIIdentityOrphaned)
		})
	}

	if _, err := c.getLocalAPIExport(logicalcluster.Name(successor.Cluster), successor.Name); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // the successor's shard performs the succession
		}
		return err
	}

	logger.Info("owner of identity is gone, passing it to the oldest holder", "successor", successor.Cluster+"|"+successor.Name)
	return c.updateStatus(ctx, entry.Name, func(entry *adminv1alpha1.APIIdentity) {
		entry.Status.Owner = successor.ExportReference
		dropHolders(entry, []adminv1alpha1.ExportReference{successor.ExportReference})
		conditions.Delete(entry, adminv1alpha1.APIIdentityOwnerMissing)
		conditions.MarkFalse(entry, adminv1alpha1.APIIdentityOrphaned, adminv1alpha1.APIIdentityOwnerSucceededReason, "", "")
	})
}

// oldestHolderInCache returns the holder that has been recorded the longest
// among those whose export is in the cache, or nil.
func (c *Controller) oldestHolderInCache(entry *adminv1alpha1.APIIdentity) *adminv1alpha1.Holder {
	holders := append([]adminv1alpha1.Holder(nil), entry.Status.Holders...)
	sort.SliceStable(holders, func(i, j int) bool {
		if !holders[i].Since.Equal(&holders[j].Since) {
			return holders[i].Since.Before(&holders[j].Since)
		}
		return holders[i].Cluster+"|"+holders[i].Name < holders[j].Cluster+"|"+holders[j].Name
	})
	for i := range holders {
		if _, err := c.getGlobalAPIExport(logicalcluster.Name(holders[i].Cluster), holders[i].Name); err == nil {
			return &holders[i]
		}
	}
	return nil
}

// goneHolders lists the holders that are not in the cache and were recorded
// longer ago than the grace period. A younger one may still be replicating.
func (c *Controller) goneHolders(entry *adminv1alpha1.APIIdentity) []adminv1alpha1.ExportReference {
	var gone []adminv1alpha1.ExportReference
	for _, holder := range entry.Status.Holders {
		if time.Since(holder.Since.Time) < MissingGracePeriod {
			continue
		}
		if _, err := c.getGlobalAPIExport(logicalcluster.Name(holder.Cluster), holder.Name); apierrors.IsNotFound(err) {
			gone = append(gone, holder.ExportReference)
		}
	}
	return gone
}

func holderRefs(entry *adminv1alpha1.APIIdentity) []adminv1alpha1.ExportReference {
	refs := make([]adminv1alpha1.ExportReference, 0, len(entry.Status.Holders))
	for _, holder := range entry.Status.Holders {
		refs = append(refs, holder.ExportReference)
	}
	return refs
}

// dropHolders removes the given holders and keeps the Shared condition in
// step with the remaining ones.
func dropHolders(entry *adminv1alpha1.APIIdentity, refs []adminv1alpha1.ExportReference) {
	for _, r := range refs {
		removeHolder(entry, r)
	}
	if len(entry.Status.Holders) > 0 {
		conditions.MarkTrue(entry, adminv1alpha1.APIIdentityShared)
	} else {
		conditions.Delete(entry, adminv1alpha1.APIIdentityShared)
	}
}

func (c *Controller) updateStatus(ctx context.Context, name string, mutate func(*adminv1alpha1.APIIdentity)) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, err := c.registry.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		desired := current.DeepCopy()
		mutate(desired)
		if equality.Semantic.DeepEqual(current.Status, desired.Status) {
			return nil
		}
		_, err = c.registry.Update(ctx, desired, metav1.UpdateOptions{})
		return err
	})
}

func ref(export *apisv1alpha2.APIExport) adminv1alpha1.ExportReference {
	return adminv1alpha1.ExportReference{Cluster: logicalcluster.From(export).String(), Name: export.Name, UID: string(export.UID)}
}

func removeHolder(entry *adminv1alpha1.APIIdentity, r adminv1alpha1.ExportReference) {
	for i := range entry.Status.Holders {
		if entry.Status.Holders[i].Equal(r) {
			entry.Status.Holders = append(entry.Status.Holders[:i], entry.Status.Holders[i+1:]...)
			return
		}
	}
}
