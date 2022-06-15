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

package clusterworkspace

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

type schedulingReconciler struct {
	getShard   func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error)
	listShards func(selector labels.Selector) ([]*tenancyv1alpha1.ClusterWorkspaceShard, error)
}

func (r *schedulingReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	workspaceClusterName := logicalcluster.From(workspace)
	switch workspace.Status.Phase {
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		// possibly de-schedule while still in scheduling phase
		if current := workspace.Status.Location.Current; current != "" {
			// make sure current shard still exists
			if shard, err := r.getShard(current); errors.IsNotFound(err) {
				klog.Infof("De-scheduling workspace %s|%s from nonexistent shard %q", tenancyv1alpha1.RootCluster, workspace.Name, current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			} else if err != nil {
				return reconcileStatusStop, err
			} else if valid, _, _ := isValidShard(shard); !valid {
				klog.Infof("De-scheduling workspace %s|%s from invalid shard %q", tenancyv1alpha1.RootCluster, workspace.Name, current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			}
		}

		if workspace.Status.Location.Current == "" {
			selector := labels.Everything()

			// find a shard for this workspace, randomly
			var err error
			shards, err := r.listShards(selector)
			if err != nil {
				return reconcileStatusStop, err
			}

			validShards := make([]*tenancyv1alpha1.ClusterWorkspaceShard, 0, len(shards))
			invalidShards := map[string]struct {
				reason, message string
			}{}
			for _, shard := range shards {
				if valid, reason, message := isValidShard(shard); valid {
					validShards = append(validShards, shard)
				} else {
					invalidShards[shard.Name] = struct {
						reason, message string
					}{
						reason:  reason,
						message: message,
					}
				}
			}

			if len(validShards) > 0 {
				targetShard := validShards[rand.Intn(len(validShards))]

				u, err := url.Parse(targetShard.Spec.ExternalURL)
				if err != nil {
					// shouldn't happen since we just checked in isValidShard
					conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonReasonUnknown, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target ClusterWorkspaceShard: %v.", err)
					return reconcileStatusStop, err // requeue
				}
				u.Path = path.Join(u.Path, workspaceClusterName.Join(workspace.Name).Path())

				workspace.Status.BaseURL = u.String()
				workspace.Status.Location.Current = targetShard.Name

				conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				klog.Infof("Scheduled workspace %s|%s to %s|%s", workspaceClusterName, workspace.Name, logicalcluster.From(targetShard), targetShard.Name)
			} else {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "No available shards to schedule the workspace.")
				failures := make([]string, 0, len(invalidShards))
				for name, x := range invalidShards {
					failures = append(failures, fmt.Sprintf("  %s: reason %q, message %q", name, x.reason, x.message))
				}
				klog.Infof("No valid shards found for workspace %s|%s, skipped:\n%s", workspaceClusterName, workspace.Name, strings.Join(failures, "\n"))
			}
		}
	case tenancyv1alpha1.ClusterWorkspacePhaseInitializing, tenancyv1alpha1.ClusterWorkspacePhaseReady:
		// movement can only happen after scheduling
		if workspace.Status.Location.Target == "" {
			break
		}

		current, target := workspace.Status.Location.Current, workspace.Status.Location.Target
		if current == target {
			workspace.Status.Location.Target = ""
			break
		}

		_, err := r.getShard(target)
		if errors.IsNotFound(err) {
			klog.Infof("Cannot move to nonexistent shard %q", tenancyv1alpha1.RootCluster, workspace.Name, target)
		} else if err != nil {
			return reconcileStatusStop, err
		}

		klog.Infof("Moving workspace %q to %q", workspace.Name, workspace.Status.Location.Target)
		workspace.Status.Location.Current = workspace.Status.Location.Target
		workspace.Status.Location.Target = ""
	}

	// check scheduled shard. This has no influence on the workspace baseURL or shard assignment. This might be a trigger for
	// a movement controller in the future (or a human intervention) to move workspaces off a shard.
	if workspace.Status.Location.Current != "" {
		if shard, err := r.getShard(workspace.Status.Location.Current); errors.IsNotFound(err) {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, tenancyv1alpha1.WorkspaceShardValidReasonShardNotFound, conditionsv1alpha1.ConditionSeverityError, "ClusterWorkspaceShard %q got deleted.", workspace.Status.Location.Current)
		} else if err != nil {
			return reconcileStatusStop, err
		} else if valid, reason, message := isValidShard(shard); !valid {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, reason, conditionsv1alpha1.ConditionSeverityError, message)
		} else {
			conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceShardValid)
		}
	}

	return reconcileStatusContinue, nil
}

func isValidShard(shard *tenancyv1alpha1.ClusterWorkspaceShard) (valid bool, reason, message string) {
	return true, "", ""
}
