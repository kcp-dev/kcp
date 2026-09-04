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

// Package identitymigrator implements the storage-level building block of
// APIExport identity rotation (enhancements KEP 0005): a per-shard
// controller that drains bound resource instances from old identity etcd
// prefixes onto the current one.
//
// It acts on any APIBinding on its shard whose boundResources carry more
// than one identity hash. The drain target is the resource's current
// schema.identityHash; every other entry in identityHashes is a drain
// source. For each affected workspace it fences the LogicalCluster with the
// core.kcp.io/inactive maintenance annotation, copies all keys of the source
// prefixes onto the target prefix via raw storage handles (preserving object
// bytes - UIDs, status, ownerReferences - verbatim), verifies counts,
// deletes the source prefixes, prunes identityHashes and lifts the fence.
// Because identityHashes keeps every hash until the drain is verified, a
// crashed migrator resumes idempotently.
package identitymigrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	ControllerName = "kcp-identity-migrator"

	// copyPageSize is the number of keys fetched per page during a drain.
	copyPageSize = int64(500)

	// rebuiltForIdentityAnnotationKey marks a bound CRD as recreated by the
	// migrator for a given identity hash. The apiextensions serving storage
	// caches the identity etcd prefix per CRD object (keyed by its UID) and
	// never observes binding-level identity changes, so a drain must recreate
	// the bound CRD to force a storage rebuild. The marker makes that
	// recreation idempotent across syncs and crashes.
	rebuiltForIdentityAnnotationKey = "internal.apis.kcp.io/identity-rebuild"
)

// NewController returns the per-shard identity migrator.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	etcdClient *clientv3.Client,
	etcdStoragePrefix string,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
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
		etcdClient:        etcdClient,
		etcdStoragePrefix: strings.TrimSuffix(etcdStoragePrefix, "/"),
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
		deleteBoundCRD: func(ctx context.Context, name string, uid types.UID) error {
			return crdClusterClient.Cluster(apibinding.SystemBoundCRDsClusterName.Path()).ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, name, metav1.DeleteOptions{
				Preconditions: &metav1.Preconditions{UID: &uid},
			})
		},
	}

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

// Controller drains bound resource instances from old identity prefixes.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	etcdClient        *clientv3.Client
	etcdStoragePrefix string

	getAPIBinding          func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport           func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	updateAPIBindingStatus func(ctx context.Context, cluster logicalcluster.Path, binding *apisv1alpha2.APIBinding) (*apisv1alpha2.APIBinding, error)
	getLogicalCluster      func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	updateLogicalCluster   func(ctx context.Context, cluster logicalcluster.Path, lc *corev1alpha1.LogicalCluster) (*corev1alpha1.LogicalCluster, error)
	listAPIBindings        func() ([]*apisv1alpha2.APIBinding, error)
	getAPIResourceSchema   func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getBoundCRD            func(name string) (*apiextensionsv1.CustomResourceDefinition, error)
	createBoundCRD         func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error
	deleteBoundCRD         func(ctx context.Context, name string, uid types.UID) error
}

func (c *Controller) enqueue(obj interface{}) {
	binding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return
	}
	if !needsDrain(binding) {
		return
	}
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key).V(4).Info("queueing APIBinding for identity drain")
	c.queue.Add(key)
}

// needsDrain reports whether any bound resource of the binding lists an
// identity hash besides its current schema identity.
func needsDrain(binding *apisv1alpha2.APIBinding) bool {
	for _, br := range binding.Status.BoundResources {
		for _, hash := range br.IdentityHashes {
			if hash != br.Schema.IdentityHash {
				return true
			}
		}
	}
	return false
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

	binding, err := c.getAPIBinding(clusterName, name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !needsDrain(binding) {
		return nil
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

	// the drain target is the owning export's current identity: the binding
	// keeps serving the old identity (schema.identityHash) until the copy is
	// verified, then flips.
	exportPath := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
	if exportPath.Empty() {
		exportPath = clusterName.Path()
	}
	apiExport, err := c.getAPIExport(exportPath, binding.Spec.Reference.Export.Name)
	if err != nil {
		return fmt.Errorf("failed to resolve APIExport %s|%s for identity drain: %w", exportPath, binding.Spec.Reference.Export.Name, err)
	}
	target := apiExport.Status.IdentityHash
	if target == "" {
		return fmt.Errorf("APIExport %s|%s has no identity yet", exportPath, binding.Spec.Reference.Export.Name)
	}

	// 2. copy and verify every bound resource's sources onto the target
	// while the old identity still serves (fenced, so nobody observes the
	// intermediate state).
	binding = binding.DeepCopy()
	var errs []error
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		sources := sets.New(br.IdentityHashes...).Insert(br.Schema.IdentityHash).Delete(target)
		if sources.Len() == 0 || !sets.New(br.IdentityHashes...).Has(target) {
			continue
		}
		for _, source := range sets.List(sources) {
			if err := c.copyPrefix(ctx, br.Group, br.Resource, source, target, clusterName); err != nil {
				errs = append(errs, fmt.Errorf("draining %s.%s of %s from identity %s to %s: %w", br.Resource, br.Group, clusterName, source, target, err))
			}
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	// 3. flip serving: the bound CRDs are synthesized from
	// schema.identityHash, so this status write switches reads and writes to
	// the fully-copied target prefix.
	flipped := false
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		if sets.New(br.IdentityHashes...).Has(target) && br.Schema.IdentityHash != target {
			br.Schema.IdentityHash = target
			flipped = true
		}
	}
	if flipped {
		if binding, err = c.updateAPIBindingStatus(ctx, clusterName.Path(), binding); err != nil {
			return err
		}
		binding = binding.DeepCopy()
	}

	// 3b. rebuild serving: the apiextensions serving storage caches the
	// identity etcd prefix per bound CRD object (keyed by its UID) and never
	// observes the flip above. Recreate the bound CRD (marked, new UID) so
	// the storage is rebuilt against the target identity. The CRD is shared
	// by every binding of the schema on this shard, so this waits until the
	// last of them has flipped.
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		if br.Schema.IdentityHash != target || sets.New(br.IdentityHashes...).Delete(target).Len() == 0 {
			continue
		}
		if err := c.ensureServingRebuilt(ctx, br, target, logicalcluster.From(apiExport)); err != nil {
			return err
		}
	}

	// 4. delete the drained source prefixes and prune the bookkeeping.
	pruned := false
	for i := range binding.Status.BoundResources {
		br := &binding.Status.BoundResources[i]
		sources := sets.New(br.IdentityHashes...).Delete(target)
		if sources.Len() == 0 || br.Schema.IdentityHash != target {
			continue
		}
		for _, source := range sets.List(sources) {
			if err := c.deletePrefix(ctx, br.Group, br.Resource, source, clusterName); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if len(errs) == 0 {
			br.IdentityHashes = []string{target}
			pruned = true
		}
	}
	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}
	if pruned {
		conditions.MarkTrue(binding, apisv1alpha2.IdentityMigrationCompleted)
		if binding, err = c.updateAPIBindingStatus(ctx, clusterName.Path(), binding); err != nil {
			return err
		}
	}

	// 5. everything drained: lift the fence.
	if needsDrain(binding) {
		return fmt.Errorf("bound resources of %s|%s still need draining", clusterName, name)
	}
	if _, err := c.ensureFence(ctx, clusterName, false); err != nil {
		return err
	}
	logger.V(2).Info("identity drain completed", "logicalCluster", clusterName, "apibinding", name, "identity", target)
	return nil
}

// ensureServingRebuilt makes sure the bound CRD of a flipped bound resource
// has been recreated for the target identity and is established again. The
// recreated CRD carries the rebuild marker annotation; a bound CRD without
// the current target marker was built (or may have been built) against a
// stale identity and must be replaced. Waiting states are returned as errors
// so the workqueue retries with backoff.
func (c *Controller) ensureServingRebuilt(ctx context.Context, br *apisv1alpha2.BoundAPIResource, target string, schemaCluster logicalcluster.Name) error {
	logger := klog.FromContext(ctx)

	crd, err := c.getBoundCRD(br.Schema.UID)
	switch {
	case apierrors.IsNotFound(err):
		// deleted (by us or otherwise): recreate it, marked for the target
		// identity. The apibinding reconciler skips recreation for bindings
		// mid-migration, so this create owns the rebuild.
		schema, err := c.getAPIResourceSchema(schemaCluster, br.Schema.Name)
		if err != nil {
			return fmt.Errorf("failed to get APIResourceSchema %s|%s to rebuild bound CRD %s: %w", schemaCluster, br.Schema.Name, br.Schema.UID, err)
		}
		newCRD, err := apibinding.GenerateBoundCRD(schema)
		if err != nil {
			return err
		}
		newCRD.Annotations[rebuiltForIdentityAnnotationKey] = target
		if err := c.createBoundCRD(ctx, newCRD); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		logger.V(2).Info("recreated bound CRD for rotated identity", "crd", br.Schema.UID, "identity", target)
		return fmt.Errorf("waiting for recreated bound CRD %s to be established", br.Schema.UID)
	case err != nil:
		return err
	}

	if crd.Annotations[rebuiltForIdentityAnnotationKey] == target {
		if !apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			return fmt.Errorf("waiting for bound CRD %s to be established", br.Schema.UID)
		}
		return nil
	}

	if !crd.DeletionTimestamp.IsZero() {
		return fmt.Errorf("waiting for stale bound CRD %s to finish deleting", br.Schema.UID)
	}

	// Deleting the CRD runs the apiextensions cleanup finalizer against the
	// storage built from the pre-rotation identity, wiping the old prefixes.
	// That is only safe once every binding of this schema on the shard has
	// flipped (each flip happens only after its copy is verified).
	bindings, err := c.listAPIBindings()
	if err != nil {
		return err
	}
	for _, other := range bindings {
		for _, obr := range other.Status.BoundResources {
			if obr.Schema.UID == br.Schema.UID && obr.Schema.IdentityHash != target {
				return fmt.Errorf("bound CRD %s is shared with APIBinding %s|%s still serving identity %s; waiting for its drain",
					br.Schema.UID, logicalcluster.From(other), other.Name, obr.Schema.IdentityHash)
			}
		}
	}

	if err := c.deleteBoundCRD(ctx, crd.Name, crd.UID); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return err
	}
	logger.V(2).Info("deleted stale bound CRD to rebuild serving storage", "crd", br.Schema.UID, "identity", target)
	return fmt.Errorf("recreating bound CRD %s for identity %s", br.Schema.UID, target)
}

// ensureFence sets or removes the inactive maintenance annotation on the
// workspace's LogicalCluster and reports whether the desired state is
// reached. Marking a binding mid-drain also sets the
// IdentityMigrationCompleted=False condition while fencing.
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

// copyPrefix copies every key of the (group, resource, source identity,
// cluster) etcd prefix onto the target identity prefix and verifies the
// counts. Object bytes are preserved verbatim; only resourceVersions change
// (new etcd revisions). Idempotent: re-copying overwrites with identical
// bytes.
func (c *Controller) copyPrefix(ctx context.Context, group, resource, source, target string, clusterName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	sourcePrefix := c.identityPrefix(group, resource, source, clusterName)
	targetPrefix := c.identityPrefix(group, resource, target, clusterName)

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
			targetKey := targetPrefix + strings.TrimPrefix(string(kv.Key), sourcePrefix)
			if _, err := c.etcdClient.Put(ctx, targetKey, string(kv.Value)); err != nil {
				return fmt.Errorf("failed to copy %q to %q: %w", string(kv.Key), targetKey, err)
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
	logger.V(2).Info("copied identity prefix", "source", sourcePrefix, "target", targetPrefix, "keys", copied)
	return nil
}

// deletePrefix removes a fully drained source identity prefix.
func (c *Controller) deletePrefix(ctx context.Context, group, resource, source string, clusterName logicalcluster.Name) error {
	sourcePrefix := c.identityPrefix(group, resource, source, clusterName)
	if _, err := c.etcdClient.Delete(ctx, sourcePrefix, clientv3.WithPrefix()); err != nil {
		return fmt.Errorf("failed to delete drained source prefix %q: %w", sourcePrefix, err)
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

// identityPrefix is the etcd prefix of a bound resource's instances in one
// logical cluster under one identity:
//
//	<storage-prefix>/<group>/<resource>/<identityHash>/<cluster>/
func (c *Controller) identityPrefix(group, resource, identityHash string, clusterName logicalcluster.Name) string {
	return strings.Join([]string{c.etcdStoragePrefix, group, resource, identityHash, clusterName.String()}, "/") + "/"
}
