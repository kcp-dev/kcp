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

package logicalclustermigration

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func (c *Controller) reconcile(ctx context.Context, migration *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	logger := klog.FromContext(ctx)

	isOrigin := c.isOrigin(migration)
	isDestination := c.isDestination(migration)

	if !isOrigin && !isDestination {
		logger.V(4).Info("migration is not for this shard, skipping")
		return false, nil
	}

	// Route through the process. The overview of the process id
	// described in the doc.go, details are in the methods.
	switch migration.Status.Phase {
	case migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migrationv1alpha1.LogicalClusterMigrationPhaseFailed:
		return false, nil

	case "":
		if isOrigin {
			migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhasePreparing
			return true, nil
		}
		return false, nil

	case migrationv1alpha1.LogicalClusterMigrationPhasePreparing:
		if isOrigin {
			return c.reconcilePreparing(ctx, migration)
		}
		return false, nil

	case migrationv1alpha1.LogicalClusterMigrationPhaseMigrating:
		if isDestination {
			return c.reconcileMigrating(ctx, migration)
		}
		return false, nil

	case migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup:
		if isOrigin {
			return c.reconcileOriginCleanup(ctx, migration)
		}
		return false, nil

	case migrationv1alpha1.LogicalClusterMigrationPhaseDestinationFinalize:
		if isDestination {
			return c.reconcileDestinationFinalize(ctx, migration)
		}
		return false, nil

	default:
		logger.V(2).Info("unknown phase", "phase", migration.Status.Phase)
		return false, nil
	}
}

func (c *Controller) reconcilePreparing(ctx context.Context, migration *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	logger := klog.FromContext(ctx)
	lcName := logicalcluster.Name(migration.Spec.LogicalCluster)

	logger.V(2).Info("preparing origin for migration", "logicalCluster", lcName)

	// Set the origin shard in the status first and requeue to ensure it is set,
	// otherwise the process stalls if an error happens intermittently as the LC
	// will be purged from the informer stores.
	if migration.Status.OriginShard == "" {
		migration.Status.OriginShard = c.shardName
		return true, nil
	}

	// Annotate the LC
	lc, err := c.logicalClusterLister.Cluster(lcName).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return true, fmt.Errorf("failed to get LogicalCluster %s: %w", lcName, err)
	}

	migrationPath := logicalcluster.From(migration).Path().Join(migration.Name).String()

	// Verify the LC is not already migrating
	if existingAnn, ok := lc.Annotations[MigratingAnnotationKey]; ok && existingAnn != "" && existingAnn != migrationPath {
		// This is a failstate - if a migration is created for an LC
		// that already is in a migration the process cannot start.
		conditions.MarkFalse(
			migration,
			migrationv1alpha1.LCMigrationOriginReady,
			"LogicalClusterAlreadyMigrating",
			conditionsv1alpha1.ConditionSeverityError,
			"logical cluster is already migrating with %q", existingAnn,
		)
		migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseFailed
		return false, nil
	}

	// Set the migration path as the migration annotation
	if lc.Annotations[MigratingAnnotationKey] != migrationPath {
		lcCopy := lc.DeepCopy()
		if lcCopy.Annotations == nil {
			lcCopy.Annotations = make(map[string]string)
		}
		lcCopy.Annotations[MigratingAnnotationKey] = migrationPath
		if _, err := c.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(lcName.Path()).Update(ctx, lcCopy, metav1.UpdateOptions{}); err != nil {
			return true, fmt.Errorf("failed to set migrating annotation on LogicalCluster %s: %w", lcName, err)
		}
	}

	// Register in MigratingLogicalClusters, this causes the handler to
	// prevent deny access and the DDSIF to ignore objects related to
	// the LC
	if !c.migratingLogicalClusters.IsMigrating(lcName) {
		if err := c.migratingLogicalClusters.Set(lcName, migrationPath); err != nil {
			return false, err
		}
	}

	// Cancel per-cluster context to stop all requests to it
	// Just passing .Path is fine since lcName is the logical cluster id.
	c.cancelLogicalClusterConnections(lcName.Path(), errors.New("logical cluster is being migrated"))

	// Purge informer stores for this cluster
	c.ddsif.PurgeCluster(lcName)

	// Phase is done, update LCMigration
	conditions.MarkTrue(migration, migrationv1alpha1.LCMigrationOriginReady)
	migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseMigrating

	logger.V(2).Info("origin prepared, transitioning to Migrating", "logicalCluster", lcName)
	return false, nil
}

func (c *Controller) reconcileMigrating(ctx context.Context, migration *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	logger := klog.FromContext(ctx)
	lcName := logicalcluster.Name(migration.Spec.LogicalCluster)

	// Register in MigratingLogicalClusters to prevent reconciles
	migrationPath := logicalcluster.From(migration).Path().Join(migration.Name).String()
	if !c.migratingLogicalClusters.IsMigrating(lcName) {
		if err := c.migratingLogicalClusters.Set(lcName, migrationPath); err != nil {
			return false, err
		}
	}

	// Purge local informer stores - this should be a noop but just to
	// be safe.
	c.ddsif.PurgeCluster(lcName)

	// Copy one page of data per reconcile. status.dumpContinue and
	// status.entriesCopied are persisted after every call, so a shard
	// restart resumes from the last completed page instead of starting
	// the copy over.
	copied, nextContinue, err := c.copyPageFromOrigin(ctx, lcName, migration.Status.OriginShard, migration.Status.DumpContinue)
	if err != nil {
		conditions.MarkFalse(
			migration,
			migrationv1alpha1.LCMigrationDataCopied,
			"CopyFailed",
			conditionsv1alpha1.ConditionSeverityError,
			"%v", err,
		)
		return true, err
	}
	requeue := applyDumpPageResult(migration, copied, nextContinue)
	if requeue {
		logger.V(2).Info("copied data page, more remaining", "logicalCluster", lcName, "entriesCopied", migration.Status.EntriesCopied)
	} else {
		// Copy is done, release this migration's hold on the origin
		// shard's shared client. It's only actually torn down once no
		// other concurrent migration from the same origin shard is still
		// using it.
		c.releaseOriginClient(lcName, migration.Status.OriginShard)
		logger.V(2).Info("data copied, transitioning to OriginCleanup", "logicalCluster", lcName, "entriesCopied", migration.Status.EntriesCopied)
	}
	return requeue, nil
}

// applyDumpPageResult records the result of copying one LogicalClusterDump
// page onto migration.Status and reports whether reconcileMigrating should
// requeue for another page.
//
// status.entriesCopied and status.dumpContinue are updated unconditionally,
// so they're persisted by the controller's commit step after every
// reconcile - including ones where the shard is about to be restarted -
// letting the next reconcile resume from status.dumpContinue instead of
// restarting the copy.
//
// A page only reaches this function once it has been copied successfully,
// so any CopyFailed condition left over from an earlier failed attempt on
// this migration no longer applies and is cleared here - otherwise it
// would stay stuck at False while later pages keep succeeding, right up
// until the copy happens to finish.
func applyDumpPageResult(migration *migrationv1alpha1.LogicalClusterMigration, copied int64, nextContinue string) (requeue bool) {
	migration.Status.EntriesCopied += copied
	migration.Status.DumpContinue = nextContinue
	conditions.Delete(migration, migrationv1alpha1.LCMigrationDataCopied)

	if nextContinue != "" {
		return true
	}

	conditions.MarkTrue(migration, migrationv1alpha1.LCMigrationDataCopied)
	migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup
	return false
}

func (c *Controller) reconcileOriginCleanup(ctx context.Context, migration *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	logger := klog.FromContext(ctx)
	lcName := logicalcluster.Name(migration.Spec.LogicalCluster)

	if err := c.deleteOriginData(ctx, lcName); err != nil {
		conditions.MarkFalse(
			migration,
			migrationv1alpha1.LCMigrationOriginCleaned,
			"CleanupFailed",
			conditionsv1alpha1.ConditionSeverityError,
			"%v", err,
		)
		return true, err
	}

	// lc is gone from shard
	c.migratingLogicalClusters.Remove(lcName)

	// Transition to DestinationFinalize.
	conditions.MarkTrue(migration, migrationv1alpha1.LCMigrationOriginCleaned)
	migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseDestinationFinalize

	logger.V(2).Info("origin cleaned, transitioning to DestinationFinalize", "logicalCluster", lcName)
	return false, nil
}

func (c *Controller) reconcileDestinationFinalize(ctx context.Context, migration *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	logger := klog.FromContext(ctx)
	lcName := logicalcluster.Name(migration.Spec.LogicalCluster)

	// While copying the data the logical cluster object was copied as
	// well with the migration annotation which now can be dropped.
	//
	// The annotation is only used at the start of the migration to
	// ensure that there aren't two different migrations targeting the
	// same logical cluster at the same time.
	// This could also be solved by requiring the
	// LogicalClusterMigration to be a singleton within the logical
	// cluster (much like the LogicalCluster itself), but that would
	// require additional logic to still allow these events through for
	// the migration reconciler to work.
	// Bypass the lister: the local DDSIF for this shard has been ignoring
	// objects for the migrating cluster, so the LogicalCluster that was
	// just written to etcd via the dump is not yet visible through the
	// lister. Read it directly from the API instead.
	lc, err := c.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(lcName.Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err != nil {
		return true, fmt.Errorf("failed to get LogicalCluster %s: %w", lcName, err)
	}
	if lc.Annotations[MigratingAnnotationKey] != "" {
		lcCopy := lc.DeepCopy()
		delete(lcCopy.Annotations, MigratingAnnotationKey)
		if _, err := c.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(lcName.Path()).Update(ctx, lcCopy, metav1.UpdateOptions{}); err != nil {
			return true, fmt.Errorf("failed to remove migrating annotation from LogicalCluster %s: %w", lcName, err)
		}
	}

	// We need to re-create the bound CRDs for all APIBindings
	// in the LC as they are not part of the dump.
	if err := c.ensureBoundCRDs(ctx, lcName); err != nil {
		return true, fmt.Errorf("failed to ensure bound CRDs for %s: %w", lcName, err)
	}

	// Migration is done, allow access to the logical cluster.
	// Maybe needs some targeted relisting for api sharing before
	// allowing access so the stateful objects (*EndpointSlice) are
	// updated?
	c.migratingLogicalClusters.Remove(lcName)

	// Update DDSIF for kcp reconcilers to pick the LC up.
	// TODO(ntnn): This might be a bit expensive, especially in large
	// clusters. Relisting every ~10h is one thing, but if a shard
	// contains 10000 workspaces and half of them are migrated elsewhere
	// this would mean 5000 relists.
	// Manually filling the stores would be least expensive but would
	// easily break and seems hacky.
	// Maybe LCMigration allows specifying a set of LCs to migrate
	// instead of just one? Or LCMigration allows setting whether a full
	// relist should be triggered after finalizing?
	c.ddsif.ForceRelist()

	// Transition to Completed.
	conditions.MarkTrue(migration, migrationv1alpha1.LCMigrationCompleted)
	migration.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCompleted

	logger.V(2).Info("migration completed", "logicalCluster", lcName)
	return false, nil
}
