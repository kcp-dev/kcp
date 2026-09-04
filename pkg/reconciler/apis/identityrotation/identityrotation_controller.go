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

// Package identityrotation drives APIExport identity rotation (enhancements
// KEP 0005). A rotation is requested through an APIExportIdentityRotation
// object (served by the platform-owned migration.kcp.io APIExport) living
// next to the APIExport in the provider workspace.
//
// The controller is thin orchestration over per-shard machinery that already
// exists: flipping the export's identity makes every shard's apibinding
// reconciler record drain bookkeeping on its bindings, and every shard's
// identity migrator drains the storage independently. This controller only
// validates the request, performs the export-side flip (secret ref, identity
// hash, alias publication), tracks progress, and retires the alias per the
// requested policy.
package identityrotation

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
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	migrationv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/migration/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/identity"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-apiexport-identity-rotation"
)

// NewController returns the identity rotation controller. It runs on every
// shard and handles rotation objects living on that shard. The rotated
// export may live in another workspace (and on another shard): reads resolve
// through the local and cache informers, writes to the export and reads of
// the new identity secret go through path-based external clients so they
// reach the export's home shard.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	externalKcpClusterClient kcpclientset.ClusterInterface,
	externalKubeClusterClient kcpkubernetesclientset.ClusterInterface,
	rotationInformer migrationv1alpha1informers.APIExportIdentityRotationClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
) (*Controller, error) {
	c := &Controller{
		// rotations spend most of their life waiting on per-shard drains;
		// cap the per-item backoff so phase transitions follow the drains
		// promptly instead of the default exponential rate limiter.
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[string](5*time.Millisecond, 2*time.Second),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getRotation: func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportIdentityRotation, error) {
			return rotationInformer.Cluster(cluster).Lister().Get(name)
		},
		updateRotationStatus: func(ctx context.Context, cluster logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error) {
			return kcpClusterClient.Cluster(cluster).MigrationV1alpha1().APIExportIdentityRotations().UpdateStatus(ctx, rotation, metav1.UpdateOptions{})
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		updateAPIExport: func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			return externalKcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		},
		updateAPIExportStatus: func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error) {
			return externalKcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().UpdateStatus(ctx, export, metav1.UpdateOptions{})
		},
		getSecretHash: func(ctx context.Context, path logicalcluster.Path, namespace, name string) (string, error) {
			secret, err := externalKubeClusterClient.Cluster(path).CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return identity.IdentityHash(secret)
		},
		listBindingsForExport: func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
			path := logicalcluster.From(export).Path()
			keys, err := apiBindingInformer.Informer().GetIndexer().IndexKeys(indexers.APIBindingsByAPIExport, path.Join(export.Name).String())
			if err != nil {
				return nil, err
			}
			bindings := make([]*apisv1alpha2.APIBinding, 0, len(keys))
			for _, key := range keys {
				obj, exists, err := apiBindingInformer.Informer().GetIndexer().GetByKey(key)
				if err != nil || !exists {
					continue
				}
				bindings = append(bindings, obj.(*apisv1alpha2.APIBinding))
			}
			return bindings, nil
		},
	}

	_, _ = rotationInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	// binding drains complete asynchronously; re-evaluate rotations when
	// bindings change.
	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) { c.enqueueForBinding(obj, rotationInformer) },
	})

	return c, nil
}

// Controller drives APIExportIdentityRotations through their lifecycle.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getRotation           func(cluster logicalcluster.Name, name string) (*migrationv1alpha1.APIExportIdentityRotation, error)
	updateRotationStatus  func(ctx context.Context, cluster logicalcluster.Path, rotation *migrationv1alpha1.APIExportIdentityRotation) (*migrationv1alpha1.APIExportIdentityRotation, error)
	getAPIExport          func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	updateAPIExport       func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error)
	updateAPIExportStatus func(ctx context.Context, path logicalcluster.Path, export *apisv1alpha2.APIExport) (*apisv1alpha2.APIExport, error)
	getSecretHash         func(ctx context.Context, path logicalcluster.Path, namespace, name string) (string, error)
	listBindingsForExport func(export *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error)
}

// exportPath is the workspace path the rotation's export reference resolves
// in: the referenced path, or the rotation's own workspace when empty.
func exportPath(clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) logicalcluster.Path {
	if path := logicalcluster.NewPath(rotation.Spec.Export.Path); !path.Empty() {
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

// enqueueForBinding requeues every non-terminal rotation on this shard when
// a binding changes: binding drains complete asynchronously and rotations may
// reference exports in other workspaces, so a cheap broad requeue beats
// resolving the export reference in the event handler. Rotations are rare.
func (c *Controller) enqueueForBinding(obj interface{}, rotationInformer migrationv1alpha1informers.APIExportIdentityRotationClusterInformer) {
	if _, ok := obj.(*apisv1alpha2.APIBinding); !ok {
		return
	}
	rotations, err := rotationInformer.Lister().List(labels.Everything())
	if err != nil {
		return
	}
	for _, rotation := range rotations {
		switch rotation.Status.Phase {
		case migrationv1alpha1.APIExportIdentityRotationCompleted, migrationv1alpha1.APIExportIdentityRotationFailed:
		default:
			c.enqueue(rotation)
		}
	}
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

	requeueAfter, err := c.process(ctx, key)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	if requeueAfter > 0 {
		c.queue.AddAfter(key, requeueAfter)
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (time.Duration, error) {
	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return 0, nil
	}

	rotation, err := c.getRotation(clusterName, name)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	rotation = rotation.DeepCopy()

	switch rotation.Status.Phase {
	case "", migrationv1alpha1.APIExportIdentityRotationPending:
		return 0, c.reconcilePending(ctx, clusterName, rotation)
	case migrationv1alpha1.APIExportIdentityRotationMigrating:
		return 0, c.reconcileMigrating(ctx, clusterName, rotation)
	case migrationv1alpha1.APIExportIdentityRotationAliasActive:
		return c.reconcileAliasActive(ctx, clusterName, rotation)
	default:
		return 0, nil // Completed and Failed are terminal
	}
}

// reconcilePending validates the request and performs the export-side flip:
// the new secret becomes the export's identity, the old hash is published as
// an alias, and every consumer's apibinding reconciler picks the change up
// as drain bookkeeping for the identity migrator.
func (c *Controller) reconcilePending(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	logger := klog.FromContext(ctx)

	path := exportPath(clusterName, rotation)
	export, err := c.getAPIExport(path, rotation.Spec.Export.Name)
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, clusterName, rotation, "APIExport %s|%s not found", path, rotation.Spec.Export.Name)
	}
	if err != nil {
		return err
	}
	if export.Status.IdentityHash == "" {
		return fmt.Errorf("APIExport %s|%s has no identity yet", path, rotation.Spec.Export.Name)
	}
	// all export-side operations happen in the export's workspace, which may
	// be on another shard: address it by cluster name through the external,
	// path-capable client.
	exportCluster := logicalcluster.From(export).Path()

	ref := rotation.Spec.NewIdentity.SecretRef
	if ref == nil {
		return c.fail(ctx, clusterName, rotation, "spec.newIdentity.secretRef is required")
	}
	newHash, err := c.getSecretHash(ctx, exportCluster, ref.Namespace, ref.Name)
	if apierrors.IsNotFound(err) {
		return c.fail(ctx, clusterName, rotation, "identity secret %s/%s not found; pre-create the new identity secret in the export's workspace %s", ref.Namespace, ref.Name, path)
	}
	if err != nil {
		return err
	}
	oldHash := export.Status.IdentityHash
	if newHash == oldHash {
		return c.fail(ctx, clusterName, rotation, "the new identity secret hashes to the export's current identity; rotation requires a fresh secret")
	}

	// flip the export: secret ref (spec), identity hash + alias (status).
	updatedExport := export.DeepCopy()
	updatedExport.Spec.Identity = &apisv1alpha2.Identity{SecretRef: ref}
	if updatedExport, err = c.updateAPIExport(ctx, exportCluster, updatedExport); err != nil {
		return err
	}
	updatedExport.Status.IdentityHash = newHash
	updatedExport.Status.IdentityAliasHashes = sets.List(sets.New(updatedExport.Status.IdentityAliasHashes...).Insert(oldHash))
	conditions.MarkFalse(updatedExport, apisv1alpha2.IdentityRotationInProgress, "RotationActive", conditionsv1alpha1.ConditionSeverityInfo,
		"identity rotation %s is migrating consumers from %s to %s", rotation.Name, oldHash, newHash)
	if _, err := c.updateAPIExportStatus(ctx, exportCluster, updatedExport); err != nil {
		return err
	}

	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationMigrating
	rotation.Status.OldIdentityHash = oldHash
	rotation.Status.NewIdentityHash = newHash
	conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationValid)
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	logger.V(2).Info("identity rotation started", "export", rotation.Spec.Export.Name, "path", path, "old", oldHash, "new", newHash)
	return err
}

// reconcileMigrating tracks per-binding drain progress. Note: progress is
// tracked for bindings observable on this shard; drains on other shards
// complete independently through their own migrators. Cross-shard progress
// aggregation is an alpha limitation.
func (c *Controller) reconcileMigrating(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) error {
	export, err := c.getAPIExport(exportPath(clusterName, rotation), rotation.Spec.Export.Name)
	if err != nil {
		return err
	}

	bindings, err := c.listBindingsForExport(export)
	if err != nil {
		return err
	}

	migrated := 0
	for _, binding := range bindings {
		if bindingFullyOn(binding, rotation.Status.NewIdentityHash) {
			migrated++
		}
	}

	rotation.Status.TotalBindings = int32(len(bindings))
	rotation.Status.MigratedBindings = int32(migrated)
	if migrated == len(bindings) {
		rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationAliasActive
		rotation.Status.AliasActiveTimestamp = &metav1.Time{Time: time.Now()}
		conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationDrained)
	}
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	return err
}

// bindingFullyOn reports whether every bound resource of the binding serves
// the given identity with no drain sources left.
func bindingFullyOn(binding *apisv1alpha2.APIBinding, hash string) bool {
	for _, br := range binding.Status.BoundResources {
		if br.Schema.IdentityHash != hash {
			return false
		}
		for _, h := range br.IdentityHashes {
			if h != hash {
				return false
			}
		}
	}
	return true
}

// reconcileAliasActive retires the alias per the requested policy.
func (c *Controller) reconcileAliasActive(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation) (time.Duration, error) {
	policy := rotation.Spec.AliasRetirement.Policy
	if policy == "" {
		policy = migrationv1alpha1.AliasRetirementManual
	}

	switch policy {
	case migrationv1alpha1.AliasRetirementManual:
		return 0, nil // wait for the provider to tighten the policy
	case migrationv1alpha1.AliasRetirementAfter:
		if rotation.Spec.AliasRetirement.After == nil || rotation.Status.AliasActiveTimestamp == nil {
			return 0, nil
		}
		deadline := rotation.Status.AliasActiveTimestamp.Add(rotation.Spec.AliasRetirement.After.Duration)
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining, nil
		}
	case migrationv1alpha1.AliasRetirementImmediate:
	}

	// retire: the old hash stops resolving.
	export, err := c.getAPIExport(exportPath(clusterName, rotation), rotation.Spec.Export.Name)
	if err != nil {
		return 0, err
	}
	if sets.New(export.Status.IdentityAliasHashes...).Has(rotation.Status.OldIdentityHash) {
		export = export.DeepCopy()
		export.Status.IdentityAliasHashes = sets.List(sets.New(export.Status.IdentityAliasHashes...).Delete(rotation.Status.OldIdentityHash))
		conditions.Delete(export, apisv1alpha2.IdentityRotationInProgress)
		if _, err := c.updateAPIExportStatus(ctx, logicalcluster.From(export).Path(), export); err != nil {
			return 0, err
		}
	}

	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationCompleted
	conditions.MarkTrue(rotation, migrationv1alpha1.IdentityRotationAliasRetired)
	_, err = c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	klog.FromContext(ctx).V(2).Info("identity rotation completed", "export", rotation.Spec.Export.Name, "retired", rotation.Status.OldIdentityHash)
	return 0, err
}

func (c *Controller) fail(ctx context.Context, clusterName logicalcluster.Name, rotation *migrationv1alpha1.APIExportIdentityRotation, format string, args ...interface{}) error {
	rotation.Status.Phase = migrationv1alpha1.APIExportIdentityRotationFailed
	conditions.MarkFalse(rotation, migrationv1alpha1.IdentityRotationValid, "ValidationFailed", conditionsv1alpha1.ConditionSeverityError, format, args...)
	_, err := c.updateRotationStatus(ctx, clusterName.Path(), rotation)
	return err
}
