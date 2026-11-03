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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// startCancellation is called only for deletion-marked migrations. Each phase
// has a single writer, including the handoff to rollback. Completed migrations
// and workspace-deletion aborts retain their existing semantics.
func (c *Controller) startCancellation(m *migrationv1alpha1.LogicalClusterMigration) bool {
	switch m.Status.Phase {
	case "", migrationv1alpha1.LogicalClusterMigrationPhasePreparing:
		if !c.isOrigin(m) {
			return false
		}
		m.Status.OriginShard = c.shardName
		// The destination cannot have started copying in Preparing.
		m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize
	case migrationv1alpha1.LogicalClusterMigrationPhaseMigrating:
		if !c.isDestination(m) {
			return false
		}
		m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup
	case migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup:
		if !c.isOrigin(m) {
			return false
		}
		// Cleanup may already have deleted some or all source data. Always
		// restore the entire dataset, not just the last forward-copy page.
		m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationPreparing
	case migrationv1alpha1.LogicalClusterMigrationPhaseDestinationFinalize:
		if !c.isDestination(m) {
			return false
		}
		m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationPreparing
	default:
		return false
	}
	m.Status.DumpContinue = ""
	conditions.MarkFalse(m, migrationv1alpha1.LCMigrationCancelled, "CancellationRequested", conditionsv1alpha1.ConditionSeverityInfo, "Restoring the logical cluster on its origin shard")
	return true
}

func (c *Controller) reconcileCancellationPreparing(ctx context.Context, m *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	if err := c.blockForCancellation(ctx, m); err != nil {
		return true, err
	}
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseRestoring
	return true, nil
}

func (c *Controller) reconcileRestoring(ctx context.Context, m *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	lcName := logicalcluster.Name(m.Spec.LogicalCluster)
	if err := c.registerCancellation(m); err != nil {
		return true, err
	}
	_, next, err := c.copyPageFromOrigin(ctx, lcName, m.Spec.DestinationShard, m.Status.DumpContinue)
	if err != nil {
		conditions.MarkFalse(m, migrationv1alpha1.LCMigrationCancelled, "RestoreFailed", conditionsv1alpha1.ConditionSeverityError, "%v", err)
		return true, err
	}
	m.Status.DumpContinue = next
	if next != "" {
		return true, nil
	}
	c.releaseOriginClient(lcName, m.Spec.DestinationShard)
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup
	return true, nil
}

func (c *Controller) reconcileCancellationCleanup(ctx context.Context, m *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	lcName := logicalcluster.Name(m.Spec.LogicalCluster)
	if err := c.registerCancellation(m); err != nil {
		return true, err
	}
	if err := c.deleteOriginData(ctx, lcName); err != nil {
		conditions.MarkFalse(m, migrationv1alpha1.LCMigrationCancelled, "CancellationCleanupFailed", conditionsv1alpha1.ConditionSeverityError, "%v", err)
		return true, err
	}
	c.releaseOriginClient(lcName, m.Status.OriginShard)
	c.deleteLogicalClusterContext(lcName.Path(), errors.New("migration cancelled"))
	c.migratingLogicalClusters.Remove(lcName)
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize
	return true, nil
}

// registerCancellation prevents local controllers and requests from changing
// the dataset while rollback is copying or deleting it.
func (c *Controller) registerCancellation(m *migrationv1alpha1.LogicalClusterMigration) error {
	lcName := logicalcluster.Name(m.Spec.LogicalCluster)
	path := logicalcluster.From(m).Path().Join(m.Name).String()
	_, registered := c.migratingLogicalClusters.Get(lcName)
	if err := c.migratingLogicalClusters.Set(lcName, path); err != nil {
		return err
	}
	// forward copying registers the destination without cancelling its context.
	c.cancelLogicalClusterConnections(lcName.Path(), errors.New("logical cluster migration is being cancelled"))
	if !registered {
		c.deleteLogicalClusterContext(logicalcluster.Wildcard, errors.New("logical cluster migration is being cancelled"))
		c.ddsif.PurgeCluster(lcName)
	}
	return nil
}

func (c *Controller) blockForCancellation(ctx context.Context, m *migrationv1alpha1.LogicalClusterMigration) error {
	if err := c.registerCancellation(m); err != nil {
		return err
	}
	lcName := logicalcluster.Name(m.Spec.LogicalCluster)
	client := c.localKcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(lcName.Path())
	lc, err := client.Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	path := logicalcluster.From(m).Path().Join(m.Name).String()
	if other := lc.Annotations[MigratingAnnotationKey]; other != "" && other != path {
		return fmt.Errorf("logical cluster is owned by migration %q", other)
	}
	if lc.Annotations[MigratingAnnotationKey] == path {
		return nil
	}
	lc = lc.DeepCopy()
	if lc.Annotations == nil {
		lc.Annotations = map[string]string{}
	}
	lc.Annotations[MigratingAnnotationKey] = path
	_, err = client.Update(ctx, lc, metav1.UpdateOptions{})
	return err
}

func (c *Controller) finalizeCancellation(ctx context.Context, m *migrationv1alpha1.LogicalClusterMigration) (bool, error) {
	lcName := logicalcluster.Name(m.Spec.LogicalCluster)
	path := logicalcluster.From(m).Path().Join(m.Name).String()
	client := c.localKcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(lcName.Path())
	lc, err := client.Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	// A missing LC before copying completes means workspace deletion;
	// afterward it could mean an incomplete restore, so retain the finalizer.
	if err != nil && (!apierrors.IsNotFound(err) || conditions.IsTrue(m, migrationv1alpha1.LCMigrationDataCopied)) {
		return true, err
	}
	if err == nil {
		// A cancellation in Preparing must not interfere with an earlier
		// migration that owns the same logical cluster.
		if other := lc.Annotations[MigratingAnnotationKey]; other != "" && other != path {
			m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancelled
			conditions.MarkTrue(m, migrationv1alpha1.LCMigrationCancelled)
			return true, nil
		}
		if conditions.IsTrue(m, migrationv1alpha1.LCMigrationDataCopied) {
			// Recreate the per-cluster request context for administrative API
			// reads. The migration registration and annotation still block users.
			c.deleteLogicalClusterContext(lcName.Path(), errors.New("restoring origin API access for cancellation"))
			if err := c.ensureBoundCRDs(ctx, lcName); err != nil {
				return true, err
			}
		}
		lc = lc.DeepCopy()
		delete(lc.Annotations, MigratingAnnotationKey)
		// Write even if the annotation was removed on a previous attempt:
		// the origin's update must republish its route after destination deletion.
		if _, err := client.Update(ctx, lc, metav1.UpdateOptions{}); err != nil {
			return true, err
		}
	}
	if owner, ok := c.migratingLogicalClusters.Get(lcName); ok && owner != path {
		return true, fmt.Errorf("logical cluster is owned by migration %q", owner)
	}
	c.deleteLogicalClusterContext(lcName.Path(), errors.New("migration cancellation completed"))
	relist := conditions.IsTrue(m, migrationv1alpha1.LCMigrationOriginReady) || c.migratingLogicalClusters.IsMigrating(lcName)
	c.migratingLogicalClusters.Remove(lcName)
	if relist {
		c.ddsif.ForceRelist()
	}
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancelled
	conditions.MarkTrue(m, migrationv1alpha1.LCMigrationCancelled)
	return true, nil
}
