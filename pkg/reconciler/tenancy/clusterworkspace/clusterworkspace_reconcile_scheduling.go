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

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/logging"
)

type schedulingReconciler struct {
	getShard                  func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error)
	listShards                func(selector labels.Selector) ([]*tenancyv1alpha1.ClusterWorkspaceShard, error)
	logicalClusterAdminConfig *restclient.Config
}

func (r *schedulingReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	workspaceClusterName := logicalcluster.From(workspace)
	switch workspace.Status.Phase {
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		// possibly de-schedule while still in scheduling phase
		if current := workspace.Status.Location.Current; current != "" {
			// make sure current shard still exists
			if shard, err := r.getShard(current); apierrors.IsNotFound(err) {
				logger.Info("de-scheduling workspace from nonexistent shard", "ClusterWorkspaceShard", current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			} else if err != nil {
				return reconcileStatusStopAndRequeue, err
			} else if valid, _, _ := isValidShard(shard); !valid {
				logger.Info("de-scheduling workspace from invalid shard", "ClusterWorkspaceShard", current)
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
			}
		}

		if workspace.Status.Location.Current == "" {
			selector := labels.Everything()
			var shards []*tenancyv1alpha1.ClusterWorkspaceShard
			if workspace.Spec.Shard != nil {
				if workspace.Spec.Shard.Selector != nil {
					var err error
					selector, err = metav1.LabelSelectorAsSelector(workspace.Spec.Shard.Selector)
					if err != nil {
						conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "spec.location.selector is invalid: %v", err)
						return reconcileStatusContinue, nil // don't retry, cannot do anything useful
					}
				}
				if shardName := workspace.Spec.Shard.Name; shardName != "" {
					shard, err := r.getShard(workspace.Spec.Shard.Name)
					if err != nil && !apierrors.IsNotFound(err) {
						return reconcileStatusStopAndRequeue, err
					}
					if apierrors.IsNotFound(err) {
						conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "shard %q specified in spec.location.name does not exist: %v", shardName, err)
						return reconcileStatusContinue, nil // retry is automatic when new shards show up
					}
					shards = []*tenancyv1alpha1.ClusterWorkspaceShard{shard}
				}
			}

			if len(shards) == 0 {
				var err error
				shards, err = r.listShards(selector)
				if err != nil {
					return reconcileStatusStopAndRequeue, err
				}

				// if no specific shard was required,
				// we are going to schedule the given ws onto the root shard
				// this step is temporary until working with multi-shard env works
				// until then we need to assign ws to the root shard otherwise all e2e test will break
				//
				// note if there are no shards just let it run, at the end, we set a proper condition.
				if len(shards) > 0 && (workspace.Spec.Shard == nil || workspace.Spec.Shard.Selector == nil) {
					// trim the list to contain only the "root" shard so that we always schedule onto it
					for _, shard := range shards {
						if shard.Name == "root" {
							shards = []*tenancyv1alpha1.ClusterWorkspaceShard{shard}
							break
						}
					}
					if len(shards) == 0 {
						names := make([]string, 0, len(shards))
						for _, shard := range shards {
							names = append(names, shard.Name)
						}
						return reconcileStatusStopAndRequeue, fmt.Errorf("since no specific shard was requested we default to schedule onto the root shard, but the root shard wasn't found, found shards: %v", names)
					}
				}
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
					return reconcileStatusStopAndRequeue, err // requeue
				}
				u.Path = path.Join(u.Path, workspaceClusterName.Join(workspace.Name).Path())

				workspace.Status.BaseURL = u.String()
				workspace.Status.Location.Current = targetShard.Name

				conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
				logging.WithObject(logger, targetShard).Info("scheduled workspace to shard")
			} else {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "No available shards to schedule the workspace.")
				failures := make([]error, 0, len(invalidShards))
				for name, x := range invalidShards {
					failures = append(failures, fmt.Errorf("  %s: reason %q, message %q", name, x.reason, x.message))
				}
				logger.Error(utilerrors.NewAggregate(failures), "no valid shards found for workspace, skipping")
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
		if apierrors.IsNotFound(err) {
			logger.Info("cannot move to nonexistent shard", "ClusterWorkspaceShard", target)
		} else if err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		logger.Info("moving workspace to shard", "ClusterWorkspaceShard", workspace.Status.Location.Target)
		workspace.Status.Location.Current = workspace.Status.Location.Target
		workspace.Status.Location.Target = ""
	}

	// check scheduled shard. This has no influence on the workspace baseURL or shard assignment. This might be a trigger for
	// a movement controller in the future (or a human intervention) to move workspaces off a shard.
	if workspace.Status.Location.Current != "" {
		shard, err := r.getShard(workspace.Status.Location.Current)
		if apierrors.IsNotFound(err) {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, tenancyv1alpha1.WorkspaceShardValidReasonShardNotFound, conditionsv1alpha1.ConditionSeverityError, "ClusterWorkspaceShard %q got deleted.", workspace.Status.Location.Current)
		} else if err != nil {
			return reconcileStatusStopAndRequeue, err
		} else if valid, reason, message := isValidShard(shard); !valid {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceShardValid, reason, conditionsv1alpha1.ConditionSeverityError, message)
		} else {
			conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceShardValid)
		}

		if workspace.Spec.Shard != nil && shard != nil {
			needsRescheduling := false
			if workspace.Spec.Shard.Selector != nil {
				var err error
				selector, err := metav1.LabelSelectorAsSelector(workspace.Spec.Shard.Selector)
				if err != nil {
					conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "spec.location.shardSelector is invalid: %v", err)
					return reconcileStatusContinue, nil // don't retry, cannot do anything useful
				}
				needsRescheduling = !selector.Matches(labels.Set(shard.Labels))
			} else if shardName := workspace.Spec.Shard.Name; shardName != "" && shardName != workspace.Status.Location.Current {
				needsRescheduling = true
			}
			if needsRescheduling {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnreschedulable, conditionsv1alpha1.ConditionSeverityError, "Needs rescheduling, but movement is not supported yet")
			} else {
				conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
			}
		} else {
			conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
		}
	}

	return reconcileStatusContinue, nil
}

func isValidShard(shard *tenancyv1alpha1.ClusterWorkspaceShard) (valid bool, reason, message string) {
	return true, "", ""
}
