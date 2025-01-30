/*
Copyright 2021 The KCP Authors.

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

package workspace

import (
	"context"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

type phaseReconciler struct {
	getLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)

	requeueAfter func(workspace *tenancyv1alpha1.Workspace, after time.Duration)
}

func (r *phaseReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "phase")

	switch workspace.Status.Phase {
	case corev1alpha1.LogicalClusterPhaseScheduling:
		if workspace.Spec.URL != "" && workspace.Spec.Cluster != "" {
			workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseInitializing
		}
	case corev1alpha1.LogicalClusterPhaseInitializing:
		logger = logger.WithValues("cluster", workspace.Spec.Cluster)

		logicalCluster, err := r.getLogicalCluster(ctx, logicalcluster.NewPath(workspace.Spec.Cluster))
		if err != nil && !apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if apierrors.IsNotFound(err) {
			logger.Info("LogicalCluster disappeared")
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedWorkspaceDisappeared, conditionsv1alpha1.ConditionSeverityError, "LogicalCluster disappeared")
			return reconcileStatusContinue, nil
		}

		workspace.Status.Initializers = logicalCluster.Status.Initializers

		if initializers := workspace.Status.Initializers; len(initializers) > 0 {
			after := time.Since(logicalCluster.CreationTimestamp.Time) / 5
			if maxDuration := time.Minute * 10; after > maxDuration {
				after = maxDuration
			}
			logger.V(3).Info("LogicalCluster still has initializers, requeueing", "initializers", initializers, "after", after)
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedInitializerExists, conditionsv1alpha1.ConditionSeverityInfo, "Initializers still exist: %v", workspace.Status.Initializers)
			r.requeueAfter(workspace, after)
			return reconcileStatusContinue, nil
		}

		logger.V(3).Info("LogicalCluster is ready")
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceInitialized)

	case corev1alpha1.LogicalClusterPhaseUnavailable:
		if updateTerminalConditionPhase(workspace) {
			return reconcileStatusStopAndRequeue, nil
		}
		return reconcileStatusContinue, nil

	case corev1alpha1.LogicalClusterPhaseReady:
		// On delete we need to wait for the logical cluster to be deleted
		// before we can mark the workspace as deleted.
		if !workspace.DeletionTimestamp.IsZero() {
			logger = logger.WithValues("cluster", workspace.Spec.Cluster)

			logicalCluster, err := r.getLogicalCluster(ctx, logicalcluster.NewPath(workspace.Spec.Cluster))
			if err != nil && !apierrors.IsNotFound(err) {
				return reconcileStatusStopAndRequeue, err
			} else if apierrors.IsNotFound(err) {
				logger.Info("LogicalCluster disappeared")
				conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceContentDeleted)
				return reconcileStatusContinue, nil
			}

			if !conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceContentDeleted) {
				after := time.Since(logicalCluster.CreationTimestamp.Time) / 5
				if max := time.Minute * 10; after > max {
					after = max
				}
				cond := conditions.Get(logicalCluster, tenancyv1alpha1.WorkspaceContentDeleted)
				if cond != nil {
					conditions.Set(workspace, cond)
					logger.V(3).Info("LogicalCluster is still deleting, requeueing", "reason", cond.Reason, "message", cond.Message, "after", after)
				} else {
					logger.V(3).Info("LogicalCluster is still deleting, requeueing", "after", after)
				}
				r.requeueAfter(workspace, after)
				return reconcileStatusContinue, nil
			}

			logger.Info("workspace content is deleted")
			return reconcileStatusContinue, nil
		}

		// if workspace is ready, we check if it suppose to be ready by checking conditions.
		if updateTerminalConditionPhase(workspace) {
			logger.Info("workspace phase changed", "status", workspace.Status)
			return reconcileStatusStopAndRequeue, nil
		}
	}

	return reconcileStatusContinue, nil
}

// updateTerminalConditionPhase checks if the workspace is ready by checking conditions and sets the phase accordingly.
// It returns true if the phase was changed, false otherwise.
func updateTerminalConditionPhase(workspace *tenancyv1alpha1.Workspace) bool {
	var notReady bool
	for _, c := range workspace.Status.Conditions {
		if c.Status == v1.ConditionFalse && strings.HasPrefix(string(c.Type), "Workspace") {
			notReady = true
			break
		}
	}
	if notReady && workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseUnavailable {
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseUnavailable
		return true
	}
	if !notReady && workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseUnavailable {
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
		return true
	}
	return false
}
