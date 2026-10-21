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

// Package apiidentity maintains the APIIdentity registry (enhancements KEP
// 0008): one object per APIExport identity, derived by the shard hosting the
// owning export and written into its system:shard logical cluster, from
// where it is replicated to the cache server and shown in the Admin
// workspace. The same controller runs the one-time conversion of identities
// that were shared by several exports and retires the aliases it leaves
// behind.
package apiidentity

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	adminv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/admin/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	configshard "github.com/kcp-dev/kcp/config/shard"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/identity"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
)

const (
	ControllerName = "kcp-apiidentity"

	// resyncInterval re-enqueues every APIIdentity this shard owns, which is
	// what drives alias retirement by grace period.
	resyncInterval = 5 * time.Minute

	convertedReason = "Converted"
)

// NewController returns a controller maintaining the APIIdentity objects of
// the identities whose owning export is hosted on this shard.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiIdentityInformer adminv1alpha1informers.APIIdentityClusterInformer,
) (*Controller, error) {
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity,
	})
	indexers.AddIfNotPresentOrDie(globalAPIExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIExportByIdentity:          indexers.IndexAPIExportByIdentity,
		indexers.APIExportByStorageKey:        indexers.IndexAPIExportByStorageKey,
		indexers.APIExportByClaimedIdentities: indexers.IndexAPIExportByClaimedIdentities,
	})
	indexers.AddIfNotPresentOrDie(apiBindingInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingByStorageKey: indexers.IndexAPIBindingByStorageKey,
	})

	coreNames := sets.New(configrootphase0.CoreAPIExportNames()...)
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
		),
		kcpClusterClient: kcpClusterClient,
		localExportsByIdentity: func(id string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](apiExportInformer.Informer().GetIndexer(), indexers.APIExportByIdentity, id)
		},
		globalExportsByIdentity: func(id string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](globalAPIExportInformer.Informer().GetIndexer(), indexers.APIExportByIdentity, id)
		},
		globalExportsByStorageKey: func(key string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](globalAPIExportInformer.Informer().GetIndexer(), indexers.APIExportByStorageKey, key)
		},
		globalExportsByClaimedIdentity: func(id string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](globalAPIExportInformer.Informer().GetIndexer(), indexers.APIExportByClaimedIdentities, id)
		},
		localBindingsByStorageKey: func(key string) ([]*apisv1alpha2.APIBinding, error) {
			return indexers.ByIndex[*apisv1alpha2.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByStorageKey, key)
		},
		getAPIIdentity: func(name string) (*adminv1alpha1.APIIdentity, error) {
			return apiIdentityInformer.Lister().Cluster(configshard.SystemShardCluster).Get(name)
		},
		listAPIIdentities: func() ([]*adminv1alpha1.APIIdentity, error) {
			return apiIdentityInformer.Lister().Cluster(configshard.SystemShardCluster).List(labels.Everything())
		},
		isCore: func(export *apisv1alpha2.APIExport) bool {
			return isRootExport(export) && coreNames.Has(export.Name)
		},
		convertLegacy: func() bool {
			return kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.APIIdentityDecoupling)
		},
		now: time.Now,
	}

	exportHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueExport(obj, nil) },
		UpdateFunc: func(old, obj interface{}) { c.enqueueExport(obj, old) },
		DeleteFunc: func(obj interface{}) { c.enqueueExport(obj, nil) },
	}
	_, _ = apiExportInformer.Informer().AddEventHandler(events.WithoutSyncs(exportHandler))
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(events.WithoutSyncs(exportHandler))
	_, _ = apiIdentityInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueIdentity(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueIdentity(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueIdentity(obj) },
	}))

	return c, nil
}

// Controller maintains the APIIdentity objects of this shard's exports.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	kcpClusterClient kcpclientset.ClusterInterface

	localExportsByIdentity         func(id string) ([]*apisv1alpha2.APIExport, error)
	globalExportsByIdentity        func(id string) ([]*apisv1alpha2.APIExport, error)
	globalExportsByStorageKey      func(key string) ([]*apisv1alpha2.APIExport, error)
	globalExportsByClaimedIdentity func(id string) ([]*apisv1alpha2.APIExport, error)
	localBindingsByStorageKey      func(key string) ([]*apisv1alpha2.APIBinding, error)
	getAPIIdentity                 func(name string) (*adminv1alpha1.APIIdentity, error)
	listAPIIdentities              func() ([]*adminv1alpha1.APIIdentity, error)
	isCore                         func(*apisv1alpha2.APIExport) bool
	convertLegacy                  func() bool
	now                            func() time.Time
}

func (c *Controller) enqueueExport(obj, old interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	for _, o := range []interface{}{obj, old} {
		export, ok := o.(*apisv1alpha2.APIExport)
		if !ok {
			continue
		}
		for _, id := range identitiesOf(export) {
			c.queue.Add(id)
		}
	}
}

// identitiesOf returns every identity an export says something about: its
// own, its aliases, the prefixes it stores under and the identities it
// claims.
func identitiesOf(export *apisv1alpha2.APIExport) []string {
	ids := sets.New[string]()
	if export.Status.IdentityHash != "" {
		ids.Insert(export.Status.IdentityHash)
	}
	ids.Insert(export.Status.IdentityAliasHashes...)
	if export.Status.Storage != nil {
		for _, rs := range export.Status.Storage.Resources {
			ids.Insert(rs.Keys...)
		}
	}
	for _, claim := range export.ResolvedPermissionClaims() {
		if claim.IdentityHash != "" {
			ids.Insert(claim.IdentityHash)
		}
	}
	return sets.List(ids)
}

func (c *Controller) enqueueIdentity(obj interface{}) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	id, ok := obj.(*adminv1alpha1.APIIdentity)
	if !ok || logicalcluster.From(id) != configshard.SystemShardCluster {
		return
	}
	c.queue.Add(id.Name)
}

// Start starts the controller, which stops when ctx.Done() is closed.
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
	go wait.UntilWithContext(ctx, c.resync, resyncInterval)

	<-ctx.Done()
}

func (c *Controller) resync(context.Context) {
	ids, err := c.listAPIIdentities()
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, id := range ids {
		c.queue.Add(id.Name)
	}
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

// reconcile brings the APIIdentity object of one identity up to date, if
// this shard hosts its owner (or hosted it, for an orphaned identity), and
// runs the legacy conversion and alias retirement for exports hosted here.
func (c *Controller) reconcile(ctx context.Context, id string) error {
	logger := klog.FromContext(ctx)

	existing, err := c.getAPIIdentity(id)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		existing = nil
	}

	in, err := c.input(id, existing)
	if err != nil {
		return err
	}
	owner := Owner(in)

	switch {
	case owner == nil && existing == nil:
		// an alias, a storage key or a claimed identity with no object of its
		// own here; nothing to do.
		return nil

	case owner == nil:
		// orphaned: the owner is gone. The object stays, so that no export
		// can take the identity over while consumers' data still sits under
		// it, until no binding on this shard stores there any more.
		bindings, err := c.localBindingsByStorageKey(id)
		if err != nil {
			return err
		}
		if len(bindings) == 0 && len(in.Adopters) == 0 {
			logger.V(2).Info("deleting orphaned APIIdentity, nothing stores under it any more")
			err := c.kcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().APIIdentities().Delete(ctx, id, metav1.DeleteOptions{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		return c.write(ctx, existing, Derive(in))

	case !c.hostedHere(owner):
		if existing != nil {
			// the owner lives on another shard now (or the identity was
			// taken over by an export elsewhere while orphaned here); that
			// shard writes the object.
			logger.V(2).Info("deleting APIIdentity, the owner is hosted on another shard", "owner", key(owner))
			err := c.kcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().APIIdentities().Delete(ctx, id, metav1.DeleteOptions{})
			if apierrors.IsNotFound(err) {
				err = nil
			}
			if err != nil {
				return err
			}
		}
	default:
		if err := c.write(ctx, existing, Derive(in)); err != nil {
			return err
		}
	}

	var errs []error
	// the legacy conversion: every holder but the owner that is hosted here
	// moves onto a fresh identity, keeping the old one as an alias.
	if len(in.Holders) > 1 && c.convertLegacy() {
		if reason, _ := ConversionBlocked(in); reason == "" {
			for _, holder := range in.Holders {
				if key(holder) == key(owner) || !c.hostedHere(holder) {
					continue
				}
				if err := c.convert(ctx, holder, id); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}

	// alias retirement for the owner, if hosted here.
	if owner != nil && c.hostedHere(owner) {
		derived := Derive(in)
		if retire := RetirableAliases(derived, in.Now); len(retire) > 0 {
			if err := c.retireAliases(ctx, owner, retire); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return utilerrors.NewAggregate(errs)
}

// hostedHere reports whether the export is served by this shard, i.e. is in
// the shard-local informer.
func (c *Controller) hostedHere(export *apisv1alpha2.APIExport) bool {
	local, err := c.localExportsByIdentity(export.Status.IdentityHash)
	if err != nil {
		return false
	}
	for _, l := range local {
		if key(l) == key(export) {
			return true
		}
	}
	return false
}

// input gathers the installation-wide view of one identity.
func (c *Controller) input(id string, existing *adminv1alpha1.APIIdentity) (Input, error) {
	in := Input{Identity: id, Existing: existing, IsCore: c.isCore, Now: c.now()}

	byIdentity, err := c.globalExportsByIdentity(id)
	if err != nil {
		return Input{}, err
	}
	seen := sets.New[string]()
	for _, export := range byIdentity {
		if export.Status.IdentityHash == id && !seen.Has(key(export)) {
			seen.Insert(key(export))
			in.Holders = append(in.Holders, export)
		}
	}
	// the local view fills in exports not yet replicated (fresh ones).
	local, err := c.localExportsByIdentity(id)
	if err != nil {
		return Input{}, err
	}
	for _, export := range local {
		if export.Status.IdentityHash == id && !seen.Has(key(export)) {
			seen.Insert(key(export))
			in.Holders = append(in.Holders, export)
		}
	}

	adopters, err := c.globalExportsByStorageKey(id)
	if err != nil {
		return Input{}, err
	}
	for _, export := range adopters {
		if export.Status.IdentityHash != id {
			in.Adopters = append(in.Adopters, export)
		}
	}

	if owner := Owner(in); owner != nil {
		claimed := sets.New[string]()
		for _, alias := range owner.Status.IdentityAliasHashes {
			claimers, err := c.globalExportsByClaimedIdentity(alias)
			if err != nil {
				return Input{}, err
			}
			for _, export := range claimers {
				if !claimed.Has(key(export)) {
					claimed.Insert(key(export))
					in.Claimers = append(in.Claimers, export)
				}
			}
		}
	}
	return in, nil
}

// write creates or updates the APIIdentity object in system:shard.
func (c *Controller) write(ctx context.Context, existing, desired *adminv1alpha1.APIIdentity) error {
	logger := klog.FromContext(ctx)
	client := c.kcpClusterClient.Cluster(configshard.SystemShardCluster.Path()).AdminV1alpha1().APIIdentities()
	if existing == nil {
		desired.Annotations = map[string]string{logicalcluster.AnnotationKey: configshard.SystemShardCluster.String()}
		created, err := client.Create(ctx, desired, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return nil // picked up on the next event
		}
		if err != nil {
			return fmt.Errorf("failed to create APIIdentity %s: %w", desired.Name, err)
		}
		logger.V(2).Info("created APIIdentity", "owner", created.Status.Owner)
		// status is a subresource.
		created.Status = desired.Status
		if _, err := client.UpdateStatus(ctx, created, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to write status of APIIdentity %s: %w", desired.Name, err)
		}
		return nil
	}
	if equality.Semantic.DeepEqual(existing.Status, desired.Status) {
		return nil
	}
	if _, err := client.UpdateStatus(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status of APIIdentity %s: %w", desired.Name, err)
	}
	logger.V(2).Info("updated APIIdentity")
	return nil
}

// convert moves a non-owner holder of a legacy shared identity onto a fresh
// owner-prefixed identity, keeping the shared one as an alias. Admission
// keeps status.identityHash immutable; the one exception is a change by kcp
// on an export whose IdentityLegacyShared condition is True, so the condition
// is written first and the identity in a second write.
func (c *Controller) convert(ctx context.Context, holder *apisv1alpha2.APIExport, shared string) error {
	logger := klog.FromContext(ctx).WithValues("holder", key(holder))
	clusterName := logicalcluster.From(holder)
	exports := c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha2().APIExports()

	current, err := exports.Get(ctx, holder.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.Status.IdentityHash != shared {
		return nil // already converted
	}
	if !conditions.IsTrue(current, apisv1alpha2.IdentityLegacyShared) {
		conditions.Set(current, &conditionsv1alpha1.Condition{
			Type:     apisv1alpha2.IdentityLegacyShared,
			Status:   corev1.ConditionTrue,
			Severity: conditionsv1alpha1.ConditionSeverityWarning,
			Reason:   adminv1alpha1.APIIdentityReasonConverting,
			Message:  fmt.Sprintf("identity %s is shared with other APIExports and is being converted: this export moves onto a fresh identity, the shared one stays honored as an alias", shared),
		})
		current, err = exports.UpdateStatus(ctx, current, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to mark APIExport %s as legacy-shared: %w", key(holder), err)
		}
		logger.Info("marked APIExport as legacy-shared, converting")
	}

	fresh, err := identity.GenerateIdentity(clusterName)
	if err != nil {
		return err
	}
	current.Status.IdentityHash = fresh
	current.Status.IdentityAliasHashes = sets.List(sets.New(current.Status.IdentityAliasHashes...).Insert(shared))
	current.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceConverted}
	conditions.MarkFalse(current, apisv1alpha2.IdentityLegacyShared, convertedReason, conditionsv1alpha1.ConditionSeverityInfo,
		"converted from the shared identity %s, which is honored as an alias until retired", shared)
	if _, err := exports.UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to convert APIExport %s onto a fresh identity: %w", key(holder), err)
	}
	logger.Info("converted APIExport onto a fresh identity", "identity", fresh, "alias", shared)
	return nil
}

// retireAliases removes aliases from the owner's status.
func (c *Controller) retireAliases(ctx context.Context, owner *apisv1alpha2.APIExport, retire []string) error {
	logger := klog.FromContext(ctx).WithValues("owner", key(owner))
	exports := c.kcpClusterClient.Cluster(logicalcluster.From(owner).Path()).ApisV1alpha2().APIExports()
	current, err := exports.Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	remaining := sets.New(current.Status.IdentityAliasHashes...).Delete(retire...)
	if remaining.Len() == len(current.Status.IdentityAliasHashes) {
		return nil
	}
	current.Status.IdentityAliasHashes = sets.List(remaining)
	if len(current.Status.IdentityAliasHashes) == 0 {
		current.Status.IdentityAliasHashes = nil
		conditions.Delete(current, apisv1alpha2.IdentityLegacyShared)
	}
	if _, err := exports.UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to retire identity aliases of APIExport %s: %w", key(owner), err)
	}
	logger.Info("retired identity aliases", "aliases", retire)
	return nil
}
