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

// Package storagemigrator implements the storage-level building block of
// APIExport storage re-keying (enhancements KEP 0008): a per-shard
// controller that drains bound resource instances from their current etcd
// prefix onto a target one.
//
// It acts on any APIBinding on its shard whose boundResources carry pending
// storage keys. While the owning export carries an active re-keying marker,
// the drain target is the marker's target key and the resource's current
// storage key is the drain source; otherwise the current storage key is the
// target and every pending key is a leftover source to finish deleting. For
// each affected workspace it fences the LogicalCluster with the
// core.kcp.io/inactive maintenance annotation, copies all keys of the source
// prefixes onto the target prefix via raw storage handles (preserving object
// bytes - UIDs, status, ownerReferences - verbatim), verifies counts, flips
// serving to the target, deletes the source prefixes, prunes the pending
// keys and lifts the fence. Because pendingKeys keeps every prefix until the
// drain is verified, a crashed migrator resumes idempotently.
//
// The identity of the export is never touched: schema.identityHash on the
// binding stays what it is, only storage.key moves.
package storagemigrator

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/storage/value"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	migrationv1alpha1apply "github.com/kcp-dev/sdk/client/applyconfiguration/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	migrationv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/migration/v1alpha1"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	ControllerName = "kcp-storage-migrator"

	// copyPageSize is the number of keys fetched per page during a drain.
	copyPageSize = int64(500)
)

// NewController returns the per-shard storage migrator.
func NewController(
	shardName string,
	kcpClusterClient kcpclientset.ClusterInterface,
	externalKcpClusterClient kcpclientset.ClusterInterface,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	etcdClient *clientv3.Client,
	etcdStoragePrefix string,
	resourceTransformers value.ResourceTransformers,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	globalRekeyInformer migrationv1alpha1informers.APIExportStorageRekeyClusterInformer,
) (*Controller, error) {
	c := &Controller{
		// waiting states (fence propagation, sibling drains, CRD
		// re-establishment) surface as sync errors; cap the per-item backoff
		// so a drain does not stall for minutes behind the default
		// exponential rate limiter while it waits on other bindings.
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](5*time.Millisecond, 2*time.Second),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		reportQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](5*time.Millisecond, 2*time.Second),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName + "-report",
			},
		),
		shardName:            shardName,
		etcdClient:           etcdClient,
		etcdStoragePrefix:    strings.TrimSuffix(etcdStoragePrefix, "/"),
		resourceTransformers: resourceTransformers,
		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Cluster(cluster).Lister().Get(name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		updateAPIBindingStatus: func(ctx context.Context, cluster logicalcluster.Path, binding *apisv1alpha2.APIBinding) (*apisv1alpha2.APIBinding, error) {
			return kcpClusterClient.Cluster(cluster).ApisV1alpha2().APIBindings().UpdateStatus(ctx, binding, metav1.UpdateOptions{})
		},
		getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Cluster(cluster).Lister().Get(corev1alpha1.LogicalClusterName)
		},
		updateLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path, lc *corev1alpha1.LogicalCluster) (*corev1alpha1.LogicalCluster, error) {
			return kcpClusterClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Update(ctx, lc, metav1.UpdateOptions{})
		},
		listAPIBindings: func() ([]*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Lister().List(labels.Everything())
		},
		listAPIBindingsInCluster: func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Cluster(cluster).Lister().List(labels.Everything())
		},
		getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			schema, err := apiResourceSchemaInformer.Cluster(cluster).Lister().Get(name)
			if apierrors.IsNotFound(err) {
				return globalAPIResourceSchemaInformer.Cluster(cluster).Lister().Get(name)
			}
			return schema, err
		},
		getBoundCRD: func(name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Cluster(apibinding.SystemBoundCRDsClusterName).Lister().Get(name)
		},
		createBoundCRD: func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
			_, err := crdClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
			return err
		},
		deleteBoundCRD: func(ctx context.Context, name string) error {
			return crdClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, name, metav1.DeleteOptions{})
		},
		listBindingsByBoundCRD: func(name string) ([]*apisv1alpha2.APIBinding, error) {
			return indexers.ByIndex[*apisv1alpha2.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByBoundResourceUID, name)
		},
		listBindingsForExport: func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
			// bindings are indexed under the path they referenced the
			// export by: usually the canonical workspace path, but the
			// cluster name works too, so look up both.
			keys := sets.New[string]()
			if path := logicalcluster.NewPath(export.Annotations[core.LogicalClusterPathAnnotationKey]); !path.Empty() {
				pathKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
				if err != nil {
					return nil, err
				}
				keys.Insert(pathKeys...)
			}
			clusterKeys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, logicalcluster.From(export).Path().Join(export.Name).String())
			if err != nil {
				return nil, err
			}
			keys.Insert(clusterKeys...)

			bindings := make([]*apisv1alpha2.APIBinding, 0, keys.Len())
			for _, key := range sets.List(keys) {
				obj, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil || !exists {
					continue
				}
				bindings = append(bindings, obj.(*apisv1alpha2.APIBinding))
			}
			return bindings, nil
		},
		getReportedProgress: func(_ context.Context, cluster logicalcluster.Name, rekeyName string) (*migrationv1alpha1.ShardMigrationProgress, error) {
			rekey, err := globalRekeyInformer.Lister().Cluster(cluster).Get(rekeyName)
			if err != nil {
				return nil, err
			}
			for i := range rekey.Status.Shards {
				if rekey.Status.Shards[i].Shard == shardName {
					return &rekey.Status.Shards[i], nil
				}
			}
			return nil, nil
		},
		applyShardProgress: func(ctx context.Context, cluster logicalcluster.Path, rekeyName string, counts shardCounts) error {
			entry := migrationv1alpha1apply.ShardMigrationProgress().
				WithShard(shardName).
				WithStorageKey(counts.storageKey).
				WithTotalBindings(counts.total).
				WithMigratedBindings(counts.migrated).
				WithLastUpdateTime(metav1.Now())
			rekey := migrationv1alpha1apply.APIExportStorageRekey(rekeyName).
				WithStatus(migrationv1alpha1apply.APIExportStorageRekeyStatus().WithShards(entry))
			_, err := externalKcpClusterClient.Cluster(cluster).MigrationV1alpha1().APIExportStorageRekeys().
				ApplyStatus(ctx, rekey, metav1.ApplyOptions{FieldManager: ControllerName + "-" + shardName, Force: true})
			return err
		},
	}

	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingByBoundResourceUID: indexers.IndexAPIBindingByBoundResourceUID,
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if binding, ok := obj.(*apisv1alpha2.APIBinding); ok && needsDrain(binding) {
				c.enqueue(obj)
			}
			c.enqueueReportForBinding(obj)
		},
		UpdateFunc: func(oldObj, obj interface{}) {
			// a binding leaving the drain state matters too: the workspace
			// fence is shared with sibling bindings and is released by
			// whichever drain finishes last, from a fresh view of the cache.
			oldBinding, _ := oldObj.(*apisv1alpha2.APIBinding)
			if binding, ok := obj.(*apisv1alpha2.APIBinding); ok && (needsDrain(binding) || (oldBinding != nil && needsDrain(oldBinding))) {
				c.enqueue(obj)
			}
			c.enqueueReportForBinding(obj)
		},
		DeleteFunc: func(obj interface{}) {
			// a binding deleted mid-drain must not leave its workspace fenced.
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			if binding, ok := obj.(*apisv1alpha2.APIBinding); ok && needsDrain(binding) {
				c.enqueue(obj)
			}
			c.enqueueReportForBinding(obj)
		},
	})

	// exports carry the active-rekey annotation while a drain is in
	// progress; it is how this shard learns it has to report progress, even
	// when it hosts no binding of the export at all.
	_, _ = globalRekeyInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueReportForRekey,
		UpdateFunc: func(_, obj interface{}) { c.enqueueReportForRekey(obj) },
	})
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueReportForExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueReportForExport(obj) },
	})

	return c, nil
}

// shardCounts is one shard's binding tally for a re-keyed export, evaluated
// against the re-keying's target storage key.
type shardCounts struct {
	storageKey      string
	total, migrated int32
}

// Controller drains bound resource instances from source storage prefixes.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	// reportQueue drives per-shard drain-progress reports to
	// APIExportStorageRekey objects, keyed by "<export path>|<name>".
	reportQueue workqueue.TypedRateLimitingInterface[string]

	shardName string

	etcdClient        clientv3.KV
	etcdStoragePrefix string
	// resourceTransformers are the shard's storage transformers (encryption
	// at rest). Values are decrypted under their source key and re-encrypted
	// under the target key, since AEAD providers authenticate the key.
	resourceTransformers value.ResourceTransformers

	getAPIBinding          func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport           func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	updateAPIBindingStatus func(ctx context.Context, cluster logicalcluster.Path, binding *apisv1alpha2.APIBinding) (*apisv1alpha2.APIBinding, error)
	getLogicalCluster      func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	updateLogicalCluster   func(ctx context.Context, cluster logicalcluster.Path, lc *corev1alpha1.LogicalCluster) (*corev1alpha1.LogicalCluster, error)
	listAPIBindings        func() ([]*apisv1alpha2.APIBinding, error)
	// listAPIBindingsInCluster lists the bindings of one logical cluster on
	// this shard; the drain fence on a workspace is shared by all of them.
	listAPIBindingsInCluster func(cluster logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error)
	getAPIResourceSchema     func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getBoundCRD              func(name string) (*apiextensionsv1.CustomResourceDefinition, error)
	createBoundCRD           func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error
	deleteBoundCRD           func(ctx context.Context, name string) error
	listBindingsByBoundCRD   func(name string) ([]*apisv1alpha2.APIBinding, error)
	listBindingsForExport    func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error)
	// getReportedProgress returns this shard's entry in the re-keying's
	// status.shards as replicated to the cache server, nil if it has none.
	getReportedProgress func(ctx context.Context, cluster logicalcluster.Name, rekeyName string) (*migrationv1alpha1.ShardMigrationProgress, error)
	applyShardProgress  func(ctx context.Context, cluster logicalcluster.Path, rekeyName string, counts shardCounts) error
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing APIBinding for storage drain")
	c.queue.Add(key)
}

// enqueueReportForBinding enqueues a drain-progress report for the export the
// binding is bound to. Unlike enqueue, this is not filtered on needsDrain:
// fully drained (and freshly deleted) bindings change the counts too.
func (c *Controller) enqueueReportForBinding(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	binding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return
	}
	path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = logicalcluster.From(binding).Path()
	}
	c.reportQueue.Add(path.String() + "|" + binding.Spec.Reference.Export.Name)
}

// enqueueReportForExport enqueues a drain-progress report for an export with
// an active storage re-keying. This covers shards that host no binding of the
// export: they still have to report a zero count so the drain is decidable.
func (c *Controller) enqueueReportForExport(obj interface{}) {
	export, ok := obj.(*apisv1alpha2.APIExport)
	if !ok {
		return
	}
	if export.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey] == "" {
		return
	}
	c.reportQueue.Add(logicalcluster.From(export).String() + "|" + export.Name)
}

// needsDrain reports whether any bound resource of the binding lists pending
// storage keys.
func needsDrain(binding *apisv1alpha2.APIBinding) bool {
	for _, br := range binding.Status.BoundResources {
		if len(br.Storage.PendingKeys) > 0 {
			return true
		}
	}
	return false
}

// drainPlan returns the drain target and sources of a bound resource. While
// the export carries an active re-keying covering the resource, the target
// is the re-keying's target key and the resource's current storage key is a
// source; otherwise the current storage key is the target and every pending
// key is a leftover source. The returned sources never contain the target.
func drainPlan(br *apisv1alpha2.BoundAPIResource, active *migrationv1alpha1.ActiveRekey) (string, sets.Set[string]) {
	key := br.StorageKey()
	pending := sets.New(br.Storage.PendingKeys...)
	if active != nil && active.Covers(br.Group, br.Resource) && active.TargetKey != key && pending.Has(active.TargetKey) {
		return active.TargetKey, pending.Delete(active.TargetKey).Insert(key)
	}
	return key, pending.Delete(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()
	defer c.reportQueue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	go wait.UntilWithContext(ctx, c.startReportWorker, time.Second)

	<-ctx.Done()
}

func (c *Controller) startReportWorker(ctx context.Context) {
	for c.processNextReportItem(ctx) {
	}
}

func (c *Controller) processNextReportItem(ctx context.Context) bool {
	key, quit := c.reportQueue.Get()
	if quit {
		return false
	}
	defer c.reportQueue.Done(key)

	if err := c.reportProgress(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to report drain progress for %q, err: %w", ControllerName, key, err))
		c.reportQueue.AddRateLimited(key)
		return true
	}
	c.reportQueue.Forget(key)
	return true
}

// reportProgress publishes this shard's binding tally for a re-keyed export
// into the re-keying object's status.shards, via server-side apply with a
// per-shard field manager. The key is "<export path>|<name>". Unchanged
// counts are not re-applied.
func (c *Controller) reportProgress(ctx context.Context, key string) error {
	exportPath, exportName, found := strings.Cut(key, "|")
	if !found {
		return nil
	}
	export, err := c.getAPIExport(logicalcluster.NewPath(exportPath), exportName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	active, ok := migrationv1alpha1.ParseActiveRekey(export.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey])
	if !ok {
		return nil // no active re-keying, nothing to report
	}

	bindings, err := c.listBindingsForExport(export)
	if err != nil {
		return err
	}
	// counted in int32 directly rather than converted from len(), which
	// keeps every golangci-lint version happy about overflow.
	counts := shardCounts{storageKey: active.TargetKey}
	for _, binding := range bindings {
		if counts.total == math.MaxInt32 {
			break
		}
		counts.total++
		if bindingFullyOn(binding, active, export) {
			counts.migrated++
		}
	}

	// The counts are compared with the re-keying object as replicated to
	// the cache server, not with a cache of what this shard applied last:
	// an applied entry can get lost. While migration.kcp.io itself is
	// re-keyed, the re-keying objects move to a new storage prefix, and a
	// report written to the old prefix after the copy goes with it. The
	// replica lags a little behind, which costs a redundant (idempotent)
	// apply right after a report, but every change of the replica
	// re-enqueues the report (enqueueReportForRekey), so a lost entry is
	// re-applied as soon as the moved object shows up without it. A replica
	// that is not there yet is treated like a missing entry.
	rekeyPath := logicalcluster.NewPath(active.Cluster)
	current, err := c.getReportedProgress(ctx, logicalcluster.Name(active.Cluster), active.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if current != nil && current.StorageKey == counts.storageKey && current.TotalBindings == counts.total && current.MigratedBindings == counts.migrated {
		return nil
	}

	if err := c.applyShardProgress(ctx, rekeyPath, active.Name, counts); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // re-keying is gone; nothing to report to
		}
		return err
	}
	klog.FromContext(ctx).V(3).Info("reported drain progress", "rekey", active.Cluster+"|"+active.Name, "total", counts.total, "migrated", counts.migrated)
	return nil
}

// enqueueReportForRekey re-evaluates this shard's drain-progress report
// whenever a re-keying object changes on the cache server: another shard
// reported, the re-keying controller moved on, or the object reappeared on a
// new storage prefix without this shard's entry.
func (c *Controller) enqueueReportForRekey(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	rekey, ok := obj.(*migrationv1alpha1.APIExportStorageRekey)
	if !ok || rekey.Status.Phase != migrationv1alpha1.APIExportStorageRekeyDraining {
		return
	}
	path := logicalcluster.NewPath(rekey.Spec.Export.Path)
	if path.Empty() {
		path = logicalcluster.From(rekey).Path()
	}
	c.reportQueue.Add(path.String() + "|" + rekey.Spec.Export.Name)
}

// bindingFullyOn reports whether every bound resource of the binding covered
// by the re-keying serves from its target key with no pending keys left.
// Bound resources the export no longer serves are left out: the drain never
// touches them (the apibinding reconciler records the target only for the
// resources it resolves), so counting them would hold the re-keying open
// forever on a binding that still carries a resource removed from the export.
func bindingFullyOn(binding *apisv1alpha2.APIBinding, active migrationv1alpha1.ActiveRekey, export *apisv1alpha2.APIExport) bool {
	for _, br := range binding.Status.BoundResources {
		if !active.Covers(br.Group, br.Resource) || !indexers.ExportServesGR(export, br.Group, br.Resource) {
			continue
		}
		if br.StorageKey() != active.TargetKey || len(br.Storage.PendingKeys) > 0 {
			return false
		}
	}
	return true
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

	binding, err := c.getAPIBinding(clusterName, name)
	if apierrors.IsNotFound(err) {
		// deleted mid-drain: make sure its workspace is not left fenced.
		return c.releaseFence(ctx, clusterName)
	}
	if err != nil {
		return err
	}
	if !needsDrain(binding) {
		// nothing to drain for this binding; it may have been the last one
		// holding the shared workspace fence.
		return c.releaseFence(ctx, clusterName)
	}

	// 1. fence the workspace: cancel connections and reject requests so no
	// write or watch straddles the storage flip. This is the existing
	// maintenance mechanism used by workspace migration.
	fenced, err := c.ensureFence(ctx, clusterName, true)
	if err != nil {
		return err
	}
	if !fenced {
		// fence not observable yet, retry.
		return fmt.Errorf("waiting for the %s fence on logical cluster %s", corev1alpha1.LogicalClusterInactiveAnnotationKey, clusterName)
	}

	// the drain target comes from the owning export's active re-keying
	// marker; the binding keeps serving its current storage key until the
	// copy is verified, then flips.
	exportPath := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
	if exportPath.Empty() {
		exportPath = clusterName.Path()
	}
	apiExport, err := c.getAPIExport(exportPath, binding.Spec.Reference.Export.Name)
	if err != nil {
		return fmt.Errorf("failed to resolve APIExport %s|%s for storage drain: %w", exportPath, binding.Spec.Reference.Export.Name, err)
	}
	var active *migrationv1alpha1.ActiveRekey
	if parsed, ok := migrationv1alpha1.ParseActiveRekey(apiExport.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey]); ok {
		active = &parsed
	}

	// 2. copy and verify every bound resource's sources onto its target
	// while the current storage key still serves (fenced, so nobody
	// observes the intermediate state).
	binding = binding.DeepCopy()
	var errs []error
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		target, sources := drainPlan(br, active)
		if sources.Len() == 0 {
			continue
		}
		for _, source := range sets.List(sources) {
			if err := c.copyPrefix(ctx, br.Group, br.Resource, source, target, clusterName); err != nil {
				errs = append(errs, fmt.Errorf("draining %s.%s of %s from storage key %s to %s: %w", br.Resource, br.Group, clusterName, source, target, err))
			}
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// 3. flip serving: the bound CRDs are synthesized from storage.key, so
	// this status write switches reads and writes to the fully-copied target
	// prefix. The former key becomes a pending (source) key until deleted.
	flipped := false
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		target, sources := drainPlan(br, active)
		if sources.Len() == 0 || br.StorageKey() == target {
			continue
		}
		br.Storage.Key = target
		br.Storage.PendingKeys = sets.List(sources)
		flipped = true
	}
	if flipped {
		if binding, err = c.updateAPIBindingStatus(ctx, clusterName.Path(), binding); err != nil {
			return err
		}
		binding = binding.DeepCopy()
	}

	// 3b. ensure serving: every storage key has a bound CRD object of its
	// own (see indexers.BoundCRDNameForKey), because the apiextensions
	// serving storage caches the etcd prefix per CRD object and never
	// observes the flip above. The flipped resource is served from the CRD
	// of its new key; create it if no other binding did yet and wait for it
	// to be established. The CRD of the old key stays for bindings still on
	// it and is garbage-collected once none references it.
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		if len(br.Storage.PendingKeys) == 0 {
			continue
		}
		if err := c.ensureServingCRD(ctx, br, apiExport); err != nil {
			return err
		}
	}

	// 4. delete the drained source prefixes and prune the bookkeeping.
	pruned := false
	// drained-from bound CRDs, by name, with the resource and target key they
	// served so that the target prefix can be rewritten after their deletion.
	type drainedCRD struct{ group, resource, target string }
	staleCRDs := map[string]drainedCRD{}
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		target := br.StorageKey()
		sources := sets.New(br.Storage.PendingKeys...).Delete(target)
		if sources.Len() == 0 {
			continue
		}
		for _, source := range sets.List(sources) {
			if err := c.deletePrefix(ctx, br.Group, br.Resource, source, clusterName); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if len(errs) == 0 {
			if err := c.touchPrefix(ctx, br.Group, br.Resource, target, clusterName); err != nil {
				errs = append(errs, err)
				continue
			}
			for _, source := range sets.List(sources) {
				staleCRDs[indexers.BoundCRDNameForKey(br.Schema.UID, br.Schema.IdentityHash, source)] = drainedCRD{group: br.Group, resource: br.Resource, target: target}
			}
			br.Storage.PendingKeys = nil
			pruned = true
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	if pruned {
		conditions.MarkTrue(binding, apisv1alpha2.StorageRekeyCompleted)
		if binding, err = c.updateAPIBindingStatus(ctx, clusterName.Path(), binding); err != nil {
			return err
		}
	}

	// 4b. drop the serving of the drained-from keys: a bound CRD that no
	// binding on this shard serves from any more is deleted right away.
	// Its serving storage goes with it, which ends the watches still
	// streaming from the old prefix (wildcard informers included) so that
	// they resume against the CRD of the new key. The bound CRD cleanup
	// would prune it eventually, but only after its age threshold, far too
	// late for a watch.
	//
	// A watch that sat on the old storage across several bindings' drains
	// saw their deletes, at resource versions later than the rewrites
	// (touchPrefix) of the clusters drained earlier; resuming on the new
	// storage from its last seen version, it would never be told about
	// those objects again. So once the old CRD is gone the whole target
	// prefix of the resource is rewritten once more, on this shard, which
	// puts a fresh event for every object after any delete such a watch can
	// have seen.
	for _, name := range sets.List(sets.KeySet(staleCRDs)) {
		deleted, err := c.deleteUnreferencedBoundCRD(ctx, name, binding)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if deleted {
			d := staleCRDs[name]
			if err := c.touchKeyPrefix(ctx, d.group, d.resource, d.target); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// 5. this binding is drained: lift the fence unless a sibling binding in
	// the same workspace is still draining behind it.
	if needsDrain(binding) {
		return fmt.Errorf("bound resources of %s|%s still need draining", clusterName, name)
	}
	logger.V(2).Info("storage drain completed", "logicalCluster", clusterName, "apibinding", name)
	return c.releaseFence(ctx, clusterName)
}

// deleteUnreferencedBoundCRD deletes the bound CRD if no binding on this
// shard other than the given one (whose own index entry may still name the
// CRD until its status update is observed) serves from it. It reports
// whether the CRD was deleted (or already gone).
func (c *Controller) deleteUnreferencedBoundCRD(ctx context.Context, name string, except *apisv1alpha2.APIBinding) (bool, error) {
	bindings, err := c.listBindingsByBoundCRD(name)
	if err != nil {
		return false, err
	}
	for _, b := range bindings {
		if b.Name == except.Name && logicalcluster.From(b) == logicalcluster.From(except) {
			continue
		}
		for _, br := range b.Status.BoundResources {
			for _, n := range indexers.BoundCRDNamesFor(br) {
				if n == name {
					return false, nil // still served from
				}
			}
		}
	}
	if err := c.deleteBoundCRD(ctx, name); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("failed to delete drained bound CRD %s: %w", name, err)
	}
	klog.FromContext(ctx).V(2).Info("deleted bound CRD of a drained storage key", "crd", name)
	return true, nil
}

// releaseFence lifts the drain fence of a workspace once no binding in it
// needs a drain any more. The fence is a single annotation on the
// LogicalCluster shared by every binding of the workspace, while drains run
// per binding and concurrently: releasing it from the first binding to
// finish would let writes land in a sibling's source prefix between its copy
// and its delete. Each finishing (or deleted) binding re-evaluates the whole
// workspace from the informer, so whichever finishes last lifts the fence.
func (c *Controller) releaseFence(ctx context.Context, clusterName logicalcluster.Name) error {
	bindings, err := c.listAPIBindingsInCluster(clusterName)
	if err != nil {
		return err
	}
	for _, other := range bindings {
		if needsDrain(other) {
			klog.FromContext(ctx).V(3).Info("keeping the drain fence; a sibling APIBinding still needs draining", "logicalCluster", clusterName, "apibinding", other.Name)
			return nil
		}
	}
	_, err = c.ensureFence(ctx, clusterName, false)
	if apierrors.IsNotFound(err) {
		return nil // the workspace is gone, and so is its fence
	}
	return err
}

// ensureServingCRD makes sure the bound CRD serving the bound resource from
// its current storage key exists and is established. The schema is the one
// the binding is bound to; for an adopted resource that is the source
// export's schema, found through the schema annotations recorded on the
// binding by the apibinding reconciler, so the export's cluster is tried
// first and the schema's recorded cluster second. Waiting states are
// returned as errors so the workqueue retries with backoff.
func (c *Controller) ensureServingCRD(ctx context.Context, br *apisv1alpha2.BoundAPIResource, apiExport *apisv1alpha2.APIExport) error {
	logger := klog.FromContext(ctx)
	name := indexers.BoundCRDNameFor(*br)

	crd, err := c.getBoundCRD(name)
	switch {
	case apierrors.IsNotFound(err):
		schema, err := c.findSchema(apiExport, br)
		if err != nil {
			return fmt.Errorf("failed to get APIResourceSchema %s to create bound CRD %s: %w", br.Schema.Name, name, err)
		}
		newCRD, err := apibinding.GenerateBoundCRDNamed(schema, name)
		if err != nil {
			return err
		}
		if err := c.createBoundCRD(ctx, newCRD); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		logger.V(2).Info("created bound CRD for re-keyed storage", "crd", name, "storageKey", br.StorageKey())
		return fmt.Errorf("waiting for bound CRD %s to be established", name)
	case err != nil:
		return err
	}
	if !apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
		return fmt.Errorf("waiting for bound CRD %s to be established", name)
	}
	return nil
}

// findSchema returns the APIResourceSchema a bound resource is bound to: by
// name in the export's cluster, or, for an adopted resource, in the cluster of
// the export it was adopted from.
func (c *Controller) findSchema(apiExport *apisv1alpha2.APIExport, br *apisv1alpha2.BoundAPIResource) (*apisv1alpha1.APIResourceSchema, error) {
	schema, err := c.getAPIResourceSchema(logicalcluster.From(apiExport), br.Schema.Name)
	if err == nil && string(schema.UID) == br.Schema.UID {
		return schema, nil
	}
	for _, rs := range apiExport.Spec.Resources {
		if rs.Group != br.Group || rs.Name != br.Resource || rs.Storage.Adopted == nil {
			continue
		}
		path := logicalcluster.NewPath(rs.Storage.Adopted.From.Path)
		if path.Empty() {
			path = logicalcluster.From(apiExport).Path()
		}
		source, err := c.getAPIExport(path, rs.Storage.Adopted.From.Name)
		if err != nil {
			return nil, err
		}
		return c.getAPIResourceSchema(logicalcluster.From(source), br.Schema.Name)
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("APIResourceSchema %s in %s has UID %s, bound resource records %s", br.Schema.Name, logicalcluster.From(apiExport), schema.UID, br.Schema.UID)
}

// ensureFence sets or removes the inactive maintenance annotation on the
// workspace's LogicalCluster and reports whether the desired state is
// reached.
func (c *Controller) ensureFence(ctx context.Context, clusterName logicalcluster.Name, want bool) (bool, error) {
	lc, err := c.getLogicalCluster(clusterName)
	if err != nil {
		return false, err
	}
	_, has := lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey]
	if has == want {
		return true, nil
	}

	lc = lc.DeepCopy()
	if want {
		if lc.Annotations == nil {
			lc.Annotations = map[string]string{}
		}
		lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey] = ControllerName
	} else {
		// only lift a fence this controller set; a fence owned by e.g. a
		// workspace migration must stay.
		if lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey] != ControllerName {
			return true, nil
		}
		delete(lc.Annotations, corev1alpha1.LogicalClusterInactiveAnnotationKey)
	}
	if _, err := c.updateLogicalCluster(ctx, clusterName.Path(), lc); err != nil && !apierrors.IsConflict(err) {
		return false, err
	}
	return false, nil // observe through the informer on the next sync
}

// copyPrefix copies every key of the (group, resource, source key, cluster)
// etcd prefix onto the target key prefix and verifies the counts. Object
// bytes are preserved verbatim; only resourceVersions change (new etcd
// revisions). Idempotent: re-copying overwrites with identical bytes.
func (c *Controller) copyPrefix(ctx context.Context, group, resource, source, target string, clusterName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	sourcePrefix := c.storagePrefix(group, resource, source, clusterName)
	targetPrefix := c.storagePrefix(group, resource, target, clusterName)

	var copied int64
	startKey := sourcePrefix
	for {
		resp, err := c.etcdClient.Get(ctx, startKey,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(sourcePrefix)),
			clientv3.WithLimit(copyPageSize),
			clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		)
		if err != nil {
			return fmt.Errorf("failed to page source prefix %q: %w", sourcePrefix, err)
		}
		for _, kv := range resp.Kvs {
			sourceKey := string(kv.Key)
			targetKey := targetPrefix + strings.TrimPrefix(sourceKey, sourcePrefix)
			data, err := c.retransform(ctx, sourceKey, targetKey, kv.Value)
			if err != nil {
				return err
			}
			if _, err := c.etcdClient.Put(ctx, targetKey, string(data)); err != nil {
				return fmt.Errorf("failed to copy %q to %q: %w", sourceKey, targetKey, err)
			}
			copied++
		}
		if !resp.More || len(resp.Kvs) == 0 {
			break
		}
		startKey = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}

	// verify: the target prefix must hold at least as many keys as the
	// source before serving may flip.
	sourceCount, err := c.countPrefix(ctx, sourcePrefix)
	if err != nil {
		return err
	}
	targetCount, err := c.countPrefix(ctx, targetPrefix)
	if err != nil {
		return err
	}
	if targetCount < sourceCount {
		return fmt.Errorf("verification failed for %q -> %q: %d source keys, %d target keys", sourcePrefix, targetPrefix, sourceCount, targetCount)
	}
	logger.V(2).Info("copied storage prefix", "source", sourcePrefix, "target", targetPrefix, "keys", copied)
	return nil
}

// deletePrefix removes a fully drained source prefix.
func (c *Controller) deletePrefix(ctx context.Context, group, resource, source string, clusterName logicalcluster.Name) error {
	sourcePrefix := c.storagePrefix(group, resource, source, clusterName)
	if _, err := c.etcdClient.Delete(ctx, sourcePrefix, clientv3.WithPrefix()); err != nil {
		return fmt.Errorf("failed to delete drained source prefix %q: %w", sourcePrefix, err)
	}
	return nil
}

// touchPrefix rewrites every key of the target prefix with its current
// value, after the source prefixes are gone. Wildcard partial-metadata
// storage (what every server-side wildcard informer, e.g. the permission claim
// labeler's, watches) spans all storage keys of a resource under one prefix
// and keys objects by cluster, namespace and name. To it the drain's copy and
// delete are an add and a delete of the same object, leaving the object
// missing from every such cache until something else writes it. The rewrite
// is that write: one final update, ordered after the deletes. Keys modified
// since they were read are left alone; that modification already emitted the
// event.
func (c *Controller) touchPrefix(ctx context.Context, group, resource, storageKey string, clusterName logicalcluster.Name) error {
	return c.touchRawPrefix(ctx, c.storagePrefix(group, resource, storageKey, clusterName))
}

// touchKeyPrefix rewrites every key of a storage key's prefix across all
// logical clusters of this shard, see touchPrefix.
func (c *Controller) touchKeyPrefix(ctx context.Context, group, resource, storageKey string) error {
	return c.touchRawPrefix(ctx, strings.Join([]string{c.etcdStoragePrefix, group, resource, storageKey}, "/")+"/")
}

func (c *Controller) touchRawPrefix(ctx context.Context, prefix string) error {
	startKey := prefix
	for {
		resp, err := c.etcdClient.Get(ctx, startKey,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithLimit(copyPageSize),
			clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend),
		)
		if err != nil {
			return fmt.Errorf("failed to page target prefix %q: %w", prefix, err)
		}
		for _, kv := range resp.Kvs {
			key := string(kv.Key)
			if _, err := c.etcdClient.Txn(ctx).
				If(clientv3.Compare(clientv3.ModRevision(key), "=", kv.ModRevision)).
				Then(clientv3.OpPut(key, string(kv.Value))).
				Commit(); err != nil {
				return fmt.Errorf("failed to rewrite %q: %w", key, err)
			}
		}
		if !resp.More || len(resp.Kvs) == 0 {
			break
		}
		startKey = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
	return nil
}

func (c *Controller) countPrefix(ctx context.Context, prefix string) (int64, error) {
	resp, err := c.etcdClient.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithCountOnly())
	if err != nil {
		return 0, fmt.Errorf("failed to count prefix %q: %w", prefix, err)
	}
	return resp.Count, nil
}

// retransform decodes a stored value through the shard's storage transformer
// for its source key and encodes it again for the target key. The key is
// authenticated data for AEAD encryption-at-rest providers (e.g. aesgcm), so
// bytes copied verbatim under a new key would not decrypt. Without
// encryption at rest the transformers are the identity and this is a no-op.
func (c *Controller) retransform(ctx context.Context, sourceKey, targetKey string, stored []byte) ([]byte, error) {
	transformer, err := kcpetcd.TransformerForKey(c.resourceTransformers, strings.TrimPrefix(sourceKey, c.etcdStoragePrefix+"/"))
	if err != nil {
		return nil, err
	}
	plain, _, err := transformer.TransformFromStorage(ctx, stored, value.DefaultContext(sourceKey))
	if err != nil {
		return nil, fmt.Errorf("failed to transform source key %q: %w", sourceKey, err)
	}
	data, err := transformer.TransformToStorage(ctx, plain, value.DefaultContext(targetKey))
	if err != nil {
		return nil, fmt.Errorf("failed to transform target key %q: %w", targetKey, err)
	}
	return data, nil
}

// storagePrefix is the etcd prefix of a bound resource's instances in one
// logical cluster under one storage key:
//
//	<storage-prefix>/<group>/<resource>/<storageKey>/<cluster>/
func (c *Controller) storagePrefix(group, resource, storageKey string, clusterName logicalcluster.Name) string {
	return strings.Join([]string{c.etcdStoragePrefix, group, resource, storageKey, clusterName.String()}, "/") + "/"
}
