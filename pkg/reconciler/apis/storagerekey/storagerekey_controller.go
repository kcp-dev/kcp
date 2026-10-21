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

// Package storagerekey drives APIExport storage re-keying (enhancements KEP
// 0008). A re-keying is requested through an APIExportStorageRekey object
// (served by the platform-owned migration.kcp.io APIExport) living next to
// the APIExport, or in a platform-operated workspace referencing the export
// by path.
//
// The identity of the export never changes. A re-keying moves the bound
// instances of the export's resources, in every consumer workspace, from
// the storage prefix they currently sit on to the target prefix: by default
// the export's own identity, which is the steady state after a storage
// handover between exports.
//
// The controller is thin orchestration over per-shard machinery: marking the
// export with the active re-keying makes every shard's apibinding reconciler
// record drain bookkeeping on its bindings, and every shard's storage
// migrator drains the storage independently. This controller only validates
// the request, places and clears the marker, and tracks progress.
package storagerekey

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	migrationv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/migration/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-apiexport-storage-rekey"
)

// NewController returns the storage re-keying controller. It runs on every
// shard and handles re-keying objects living on that shard. The re-keyed
// export may live in another workspace (and on another shard): reads resolve
// through the local and cache informers, writes to the export go through a
// path-based external client so they reach the export's home shard.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	externalKcpClusterClient kcpclientset.ClusterInterface,
	rekeyInformer migrationv1alpha1informers.APIExportStorageRekeyClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalShardInformer corev1alpha1informers.ShardClusterInformer,
) (*Controller, error) {
	c := &Controller{
		// re-keyings spend most of their life waiting on per-shard drains;
		// cap the per-item backoff so phase transitions follow the drains
		// promptly instead of the default exponential rate limiter.
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](5*time.Millisecond, 2*time.Second),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getRekey: func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportStorageRekey, error) {
			return rekeyInformer.Cluster(cluster).Lister().Get(name)
		},
		updateRekeyStatus: func(ctx context.Context, cluster logicalcluster.Path, rekey *migrationv1alpha1.APIExportStorageRekey) (*migrationv1alpha1.APIExportStorageRekey, error) {
			// per-shard migrators concurrently server-side-apply their
			// entries into status.shards, so plain updates conflict. Retry
			// with a fresh read, carrying over the migrator-owned shard
			// entries - this controller never writes them.
			// The retry is bounded: a persistent conflict is returned to the
			// workqueue, which re-runs the reconcile from a fresh object
			// instead of pushing a stale status indefinitely.
			client := kcpClusterClient.Cluster(cluster).MigrationV1alpha1().APIExportStorageRekeys()
			var updated *migrationv1alpha1.APIExportStorageRekey
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				var err error
				updated, err = client.UpdateStatus(ctx, rekey, metav1.UpdateOptions{})
				if !apierrors.IsConflict(err) {
					return err
				}
				fresh, getErr := client.Get(ctx, rekey.Name, metav1.GetOptions{})
				if getErr != nil {
					return getErr
				}
				shards := fresh.Status.Shards
				fresh.Status = rekey.Status
				fresh.Status.Shards = shards
				rekey = fresh
				return err
			})
			return updated, err
		},
		getAPIExport: func(ctx context.Context, path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			// the informers (the local one, then the cache server's replica)
			// resolve the workspace path to the cluster the export lives in.
			// The replica is not updatable: its resourceVersion is the cache
			// server's, so the export is read live from its shard, through
			// the path-capable external client, before it is updated.
			cached, err := indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
			if err != nil {
				return nil, err
			}
			return externalKcpClusterClient.Cluster(logicalcluster.From(cached).Path()).ApisV1alpha2().APIExports().Get(ctx, name, metav1.GetOptions{})
		},
		updateAPIExport: func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			return externalKcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		},
		listShardNames: func() ([]string, error) {
			shards, err := globalShardInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}
			names := make([]string, 0, len(shards))
			for _, shard := range shards {
				names = append(names, shard.Name)
			}
			return names, nil
		},
	}

	_, _ = rekeyInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	return c, nil
}

// Controller drives APIExportStorageRekeys through their lifecycle.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getRekey          func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportStorageRekey, error)
	listShardNames    func() ([]string, error)
	updateRekeyStatus func(ctx context.Context, cluster logicalcluster.Path, rekey *migrationv1alpha1.APIExportStorageRekey) (*migrationv1alpha1.APIExportStorageRekey, error)
	getAPIExport      func(ctx context.Context, path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	updateAPIExport   func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error)
}

// exportPath is the workspace path the re-keying's export reference resolves
// in: the referenced path, or the re-keying's own workspace when empty.
func exportPath(clusterName logicalcluster.Name, rekey *migrationv1alpha1.APIExportStorageRekey) logicalcluster.Path {
	if path := logicalcluster.NewPath(rekey.Spec.Export.Path); !path.Empty() {
		return path
	}
	return clusterName.Path()
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	c.queue.Add(key)
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

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	rekey, err := c.getRekey(clusterName, name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	rekey = rekey.DeepCopy()

	switch rekey.Status.Phase {
	case "", migrationv1alpha1.APIExportStorageRekeyPending:
		return c.reconcilePending(ctx, clusterName, rekey)
	case migrationv1alpha1.APIExportStorageRekeyDraining:
		return c.reconcileDraining(ctx, clusterName, rekey)
	default:
		return nil // Completed and Failed are terminal
	}
}

// reconcilePending validates the request and marks the export: the
// active-rekey annotation carries the target storage key to every shard,
// where the apibinding reconcilers record it as drain bookkeeping on their
// bindings and the storage migrators drain the storage.
func (c *Controller) reconcilePending(ctx context.Context, clusterName logicalcluster.Name, rekey *migrationv1alpha1.APIExportStorageRekey) error {
	logger := klog.FromContext(ctx)

	path := exportPath(clusterName, rekey)
	export, err := c.getAPIExport(ctx, path, rekey.Spec.Export.Name)
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, clusterName, rekey, "APIExport %s|%s not found", path, rekey.Spec.Export.Name)
	}
	if err != nil {
		return err
	}
	if export.Status.IdentityHash == "" {
		return fmt.Errorf("APIExport %s|%s has no identity yet", path, rekey.Spec.Export.Name)
	}
	// all export-side operations happen in the export's workspace, which may
	// be on another shard: address it by cluster name through the external,
	// path-capable client.
	exportCluster := logicalcluster.From(export).Path()

	target := rekey.Spec.TargetKey
	if target == "" {
		target = export.Status.IdentityHash
	}
	for _, gr := range rekey.Spec.Resources {
		if !exportServes(export, gr) {
			return c.fail(ctx, clusterName, rekey, "APIExport %s|%s does not serve %s.%s with CRD storage", path, rekey.Spec.Export.Name, gr.Resource, gr.Group)
		}
	}

	active := migrationv1alpha1.ActiveRekey{Cluster: clusterName.String(), Name: rekey.Name, TargetKey: target, Resources: rekey.Spec.Resources}
	if current, ok := migrationv1alpha1.ParseActiveRekey(export.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey]); ok {
		if current.Cluster != active.Cluster || current.Name != active.Name {
			// Admission enforces one active re-keying per export, but it
			// decides from an informer cache; two near-simultaneous creates
			// can both get through. The export's annotation is the
			// authoritative record of which re-keying owns the drain, so a
			// second one fails here instead of redirecting the drain
			// underneath the first.
			return c.fail(ctx, clusterName, rekey, "APIExport %s|%s already has an active storage re-keying (%s|%s); at most one re-keying per export may be active", path, rekey.Spec.Export.Name, current.Cluster, current.Name)
		}
		// a previous reconcile attempt already marked the export for this
		// re-keying but failed before recording Draining; the marker is in
		// place, carry on.
	} else {
		updatedExport := export.DeepCopy()
		if updatedExport.Annotations == nil {
			updatedExport.Annotations = map[string]string{}
		}
		updatedExport.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey] = active.String()
		if _, err := c.updateAPIExport(ctx, exportCluster, updatedExport); err != nil {
			return err
		}
	}

	rekey.Status.Phase = migrationv1alpha1.APIExportStorageRekeyDraining
	rekey.Status.TargetKey = target
	conditions.MarkTrue(rekey, migrationv1alpha1.StorageRekeyValid)
	_, err = c.updateRekeyStatus(ctx, clusterName.Path(), rekey)
	logger.V(2).Info("storage re-keying started", "export", rekey.Spec.Export.Name, "path", path, "targetKey", target)
	return err
}

// exportServes reports whether the export serves the group/resource with
// CRD storage, i.e. whether there is anything to re-key for it.
func exportServes(export *apisv1alpha2.APIExport, gr apisv1alpha2.GroupResource) bool {
	for _, rs := range export.Spec.Resources {
		if rs.Group == gr.Group && rs.Name == gr.Resource {
			return rs.Storage.CRD != nil
		}
	}
	return false
}

// reconcileDraining aggregates per-shard drain progress. Every shard's
// storage migrator reports its own bindings of the re-keyed export into
// status.shards (including shards with zero bindings). The drain is complete
// only when every shard known to the cache server has reported and every
// entry is fully migrated; the export's marker is then cleared and the
// re-keying completes.
func (c *Controller) reconcileDraining(ctx context.Context, clusterName logicalcluster.Name, rekey *migrationv1alpha1.APIExportStorageRekey) error {
	shardNames, err := c.listShardNames()
	if err != nil {
		return err
	}
	known := sets.New(shardNames...)

	// status.shards entries are owned by the per-shard migrators (written
	// via server-side apply); this controller only reads them. Entries of
	// shards that no longer exist are ignored, so a removed shard does not
	// block the drain forever. Entries evaluated against any storage key
	// other than this re-keying's target are stale and are ignored too.
	reported := sets.New[string]()
	var total, migrated int32
	drained := true
	for _, entry := range rekey.Status.Shards {
		if !known.Has(entry.Shard) || entry.StorageKey != rekey.Status.TargetKey {
			continue
		}
		reported.Insert(entry.Shard)
		total += entry.TotalBindings
		migrated += entry.MigratedBindings
		if entry.MigratedBindings != entry.TotalBindings {
			drained = false
		}
	}
	rekey.Status.TotalBindings = total
	rekey.Status.MigratedBindings = migrated

	if missing := known.Difference(reported); missing.Len() > 0 {
		conditions.MarkFalse(rekey, migrationv1alpha1.StorageRekeyDrained, "AwaitingShardReports", conditionsv1alpha1.ConditionSeverityInfo,
			"waiting for drain reports from shards: %v", sets.List(missing))
		_, err = c.updateRekeyStatus(ctx, clusterName.Path(), rekey)
		return err
	}

	if !drained {
		conditions.MarkFalse(rekey, migrationv1alpha1.StorageRekeyDrained, "Draining", conditionsv1alpha1.ConditionSeverityInfo,
			"%d of %d bindings drained", migrated, total)
		_, err = c.updateRekeyStatus(ctx, clusterName.Path(), rekey)
		return err
	}

	// the drain is over: clear the marker so the per-shard migrators stop
	// reporting and the apibinding reconcilers stop recording the target.
	export, err := c.getAPIExport(ctx, exportPath(clusterName, rekey), rekey.Spec.Export.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if current, ok := migrationv1alpha1.ParseActiveRekey(export.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey]); ok && current.Cluster == clusterName.String() && current.Name == rekey.Name {
			export = export.DeepCopy()
			delete(export.Annotations, migrationv1alpha1.ActiveRekeyAnnotationKey)
			if _, err := c.updateAPIExport(ctx, logicalcluster.From(export).Path(), export); err != nil {
				return err
			}
		}
	}

	rekey.Status.Phase = migrationv1alpha1.APIExportStorageRekeyCompleted
	conditions.MarkTrue(rekey, migrationv1alpha1.StorageRekeyDrained)
	_, err = c.updateRekeyStatus(ctx, clusterName.Path(), rekey)
	klog.FromContext(ctx).V(2).Info("storage re-keying completed", "export", rekey.Spec.Export.Name, "targetKey", rekey.Status.TargetKey)
	return err
}

func (c *Controller) fail(ctx context.Context, clusterName logicalcluster.Name, rekey *migrationv1alpha1.APIExportStorageRekey, format string, args ...interface{}) error {
	rekey.Status.Phase = migrationv1alpha1.APIExportStorageRekeyFailed
	conditions.MarkFalse(rekey, migrationv1alpha1.StorageRekeyValid, "ValidationFailed", conditionsv1alpha1.ConditionSeverityError, format, args...)
	_, err := c.updateRekeyStatus(ctx, clusterName.Path(), rekey)
	return err
}
