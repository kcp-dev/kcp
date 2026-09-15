/*
Copyright 2021 The kcp Authors.

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

package logicalcluster

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/contextmanager"
)

type phaseReconciler struct {
	clusterContextManager *contextmanager.Manager[logicalcluster.Path]
}

func (r *phaseReconciler) reconcile(ctx context.Context, workspace *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	if !workspace.DeletionTimestamp.IsZero() {
		// terminator controllers operate during the Terminating phase. Once they have all
		// removed themselves from status.terminators, transition to Deleting so that the
		// workspace content authorizer stops permitting access and the cluster can be
		// finalized.
		switch {
		case len(workspace.Status.Terminators) > 0:
			if workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseTerminating {
				workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseTerminating
				return reconcileStatusContinue, nil
			}
		default:
			if workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseDeleting {
				workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseDeleting
				// At this point access to the LC is no longer permitted, delete contexts for it.
				lcPath := logicalcluster.From(workspace).Path()
				r.clusterContextManager.Delete(lcPath, fmt.Errorf("logical cluster %s deleted", lcPath))
				return reconcileStatusContinue, nil
			}
		}
		return reconcileStatusContinue, nil
	}

	switch workspace.Status.Phase {
	case "", corev1alpha1.LogicalClusterPhaseScheduling:
		// A LogicalCluster object's placement is the shard it lives on, so its
		// existence implies scheduling is complete. Advance to Initializing and
		// stop so admission can copy Spec.Initializers to Status.Initializers
		// on the transition; the next reconcile advances to Ready if none
		// remain.
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseInitializing
		return reconcileStatusStopAndRequeue, nil
	case corev1alpha1.LogicalClusterPhaseInitializing:
		if len(workspace.Status.Initializers) > 0 {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedInitializerExists, conditionsv1alpha1.ConditionSeverityInfo, "Initializers still exist: %v", workspace.Status.Initializers)
			return reconcileStatusContinue, nil
		}

		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceInitialized)
	case corev1alpha1.LogicalClusterPhaseReady:
		if corev1alpha1.IsLogicalClusterInactive(workspace.Annotations) {
			// Connections are cancelled only once the Inactive phase has been
			// persisted, see below. Cancelling here would leak the cancelled
			// contexts if the status update fails, e.g. because the annotation
			// was removed in the meantime: the next reconcile would find the
			// logical cluster Ready and never drop them, failing every request
			// to this logical cluster and every wildcard request on the shard.
			workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseInactive
		}
	case corev1alpha1.LogicalClusterPhaseInactive:
		lcPath := logicalcluster.From(workspace).Path()
		if corev1alpha1.IsLogicalClusterInactive(workspace.Annotations) {
			if !r.clusterContextManager.IsCancelled(lcPath) {
				// Cancel active connections for this LC and keep its context
				// cancelled until it is reactivated.
				reason := fmt.Errorf("logical cluster %s inactive", lcPath)
				r.clusterContextManager.Cancel(lcPath, reason)
				// Terminate wildcard connections too, as they may watch objects
				// in this LC. The wildcard entry is deleted rather than
				// cancelled so that new wildcard requests are not blocked for
				// as long as this LC is inactive.
				r.clusterContextManager.Delete(logicalcluster.Wildcard, reason)
			}
		} else {
			workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
			// Drop the cancelled entry so the next request creates a fresh live context.
			r.clusterContextManager.Delete(lcPath, fmt.Errorf("logical cluster %s reactivated", lcPath))
		}
	}

	return reconcileStatusContinue, nil
}
