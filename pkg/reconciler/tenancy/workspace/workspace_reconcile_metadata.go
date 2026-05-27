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

package workspace

import (
	"context"
	"encoding/json"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
	getLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
	getShardByHash    func(hash string) (*corev1alpha1.Shard, error)
}

func (r *metaDataReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "metadata")
	if workspace.Spec.Mount != nil {
		return reconcileStatusContinue, nil
	}

	changed := false

	if workspace.Status.Phase == "" {
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseScheduling
		changed = true
	}

	expected := string(workspace.Status.Phase)
	if got := workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel]; got != expected {
		if workspace.Labels == nil {
			workspace.Labels = map[string]string{}
		}
		workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel] = expected
		changed = true
	}

	if workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady {
		if value, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
			var info authenticationv1.UserInfo
			err := json.Unmarshal([]byte(value), &info)
			if err != nil {
				logger.Error(err, "failed to unmarshal workspace owner annotation", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, value)
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if userOnlyValue, err := json.Marshal(authenticationv1.UserInfo{Username: info.Username}); err != nil {
				// should never happen
				logger.Error(err, "failed to marshal user info")
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if value != string(userOnlyValue) {
				workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = string(userOnlyValue)
				changed = true
			}
		}
	}

	// If the shard annotation between the LC and the Workspace has drifted then the LC moved to another shard.
	if workspace.DeletionTimestamp.IsZero() && workspace.Spec.Cluster != "" {
		// Only check for drift if there is an annotation on the workspace.
		if wsShardHash, ok := workspaceShardHash(workspace); ok {
			lc, err := r.getLogicalCluster(ctx, logicalcluster.NewPath(workspace.Spec.Cluster))
			if err != nil {
				if !apierrors.IsNotFound(err) {
					return reconcileStatusContinue, nil
				}
				// If the annotation exists we should only proceed if we see the LC.
				return reconcileStatusStopAndRequeue, err
			}

			lcShardHash := lc.Annotations[corev1alpha1.LogicalClusterShardAnnotationKey]
			if lcShardHash == "" {
				// The LC is visible but the annotation is empty, can't proceed without.
				return reconcileStatusStopAndRequeue, nil
			}

			if lcShardHash != wsShardHash {
				newShard, err := r.getShardByHash(lcShardHash)
				if err != nil {
					if !apierrors.IsNotFound(err) {
						return reconcileStatusContinue, err
					}
					return reconcileStatusStopAndRequeue, nil
				}
				logger.V(2).Info("LogicalCluster shard drift detected, retargeting workspace", "from", wsShardHash, "to", lcShardHash, "shard", newShard.Name)
				applyShardToWorkspaceMetadata(workspace, newShard)
				changed = true
			}
		}
	}

	if changed {
		// first update ObjectMeta before status
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}
