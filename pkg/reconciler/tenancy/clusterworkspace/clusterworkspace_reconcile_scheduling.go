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
	"encoding/json"
	"fmt"
	"net/url"
	"path"

	kubernetes "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/rand"
	restclient "k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion/deletion"
)

const (
	// clusterWorkspaceShardAnnotationKey keeps track on which shard ThisWorkspace must be scheduled. The value
	// is a base36(sha224) hash of the ClusterWorkspaceShard name.
	clusterWorkspaceShardAnnotationKey = "internal.tenancy.kcp.dev/shard"
)

type schedulingReconciler struct {
	getShard                  func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error)
	getShardByHash            func(hash string) (*tenancyv1alpha1.ClusterWorkspaceShard, error)
	listShards                func(selector labels.Selector) ([]*tenancyv1alpha1.ClusterWorkspaceShard, error)
	logicalClusterAdminConfig *restclient.Config
}

func (r *schedulingReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	switch workspace.Status.Phase {
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		shardNameHash, hasShard := workspace.Annotations[clusterWorkspaceShardAnnotationKey]
		if !hasShard {
			// note that the annotation can be only removed by a system:masters, so we should be fine here.
			var err error
			shardName, err := r.chooseShardAndMarkCondition(logger, workspace)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
			if len(shardName) == 0 {
				return reconcileStatusContinue, nil // retry is automatic when new shards show up
			}
			addShardAnnotation(workspace, shardName)
			if !hasThisWorkspaceFinalizer(workspace) {
				addThisWorkspaceFinalizer(workspace)
			}
			// this is the first part of our two-phase commit
			// the first phase is to pick up a shard
			return reconcileStatusContinue, nil
		}

		shard, err := r.getShardByHash(shardNameHash)
		if err != nil {
			if apierrors.IsNotFound(err) {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "chosen shard hash %q does not exist anymore: %v", shardNameHash, err)
				return reconcileStatusContinue, nil
			}
			return reconcileStatusStopAndRequeue, err
		}
		if valid, reason, message := isValidShard(shard); !valid {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "chosen shard hash %q is no longer valid, reason %q, message %q", shardNameHash, message, reason)
			return reconcileStatusContinue, nil
		}

		if !hasThisWorkspaceFinalizer(workspace) {
			// should not happen, the finalizer
			// is added during the first phase
			addThisWorkspaceFinalizer(workspace)
			return reconcileStatusContinue, nil
		}
		if err := r.createThisWorkspace(ctx, logicalcluster.From(workspace).Join(workspace.Name), shard, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		}
		if err := r.createClusterRoleBindingForThisWorkspace(ctx, logicalcluster.From(workspace).Join(workspace.Name), shard, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		}

		// now complete the second part of our two-phase commit: set location in workspace
		u, err := url.Parse(shard.Spec.ExternalURL)
		if err != nil {
			// shouldn't happen since we just checked in isValidShard
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonReasonUnknown, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target ClusterWorkspaceShard: %v.", err)
			return reconcileStatusStopAndRequeue, err // requeue
		}
		u.Path = path.Join(u.Path, logicalcluster.From(workspace).Join(workspace.Name).Path())
		workspace.Status.BaseURL = u.String()
		workspace.Status.Location.Current = shard.Name
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
		logging.WithObject(logger, shard).Info("scheduled workspace to shard")
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

func (r *schedulingReconciler) chooseShardAndMarkCondition(logger klog.Logger, workspace *tenancyv1alpha1.ClusterWorkspace) (string, error) {
	selector := labels.Everything()
	var shards []*tenancyv1alpha1.ClusterWorkspaceShard
	if workspace.Spec.Shard != nil {
		if workspace.Spec.Shard.Selector != nil {
			var err error
			selector, err = metav1.LabelSelectorAsSelector(workspace.Spec.Shard.Selector)
			if err != nil {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "spec.location.selector is invalid: %v", err)
				return "", nil // don't retry, cannot do anything useful
			}
		}
		if shardName := workspace.Spec.Shard.Name; shardName != "" {
			shard, err := r.getShard(workspace.Spec.Shard.Name)
			if err != nil && !apierrors.IsNotFound(err) {
				return "", err
			}
			if apierrors.IsNotFound(err) {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "shard %q specified in spec.location.name does not exist: %v", shardName, err)
				return "", nil // retry is automatic when new shards show up
			}
			shards = []*tenancyv1alpha1.ClusterWorkspaceShard{shard}
		}
	}

	if len(shards) == 0 {
		var err error
		shards, err = r.listShards(selector)
		if err != nil {
			return "", err
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
				return "", fmt.Errorf("since no specific shard was requested we default to schedule onto the root shard, but the root shard wasn't found, found shards: %v", names)
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

	if len(validShards) == 0 {
		conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "No available shards to schedule the workspace.")
		failures := make([]error, 0, len(invalidShards))
		for name, x := range invalidShards {
			failures = append(failures, fmt.Errorf("  %s: reason %q, message %q", name, x.reason, x.message))
		}
		logger.Error(utilerrors.NewAggregate(failures), "no valid shards found for workspace, skipping")
		return "", nil // retry is automatic when new shards show up
	}
	targetShard := validShards[rand.Intn(len(validShards))]
	return targetShard.Name, nil
}

func (r *schedulingReconciler) createThisWorkspace(ctx context.Context, cluster logicalcluster.Name, shard *tenancyv1alpha1.ClusterWorkspaceShard, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	this := &tenancyv1alpha1.ThisWorkspace{
		// TODO(p0lyn0mial): in the future we could set an UID based back-reference to ClusterWorkspace as annotation
		ObjectMeta: metav1.ObjectMeta{
			Name:       tenancyv1alpha1.ThisWorkspaceName,
			Finalizers: []string{deletion.WorkspaceFinalizer},
		},
		Spec: tenancyv1alpha1.ThisWorkspaceSpec{
			Owner: &tenancyv1alpha1.ThisWorkspaceOwner{
				APIVersion: tenancyv1beta1.SchemeGroupVersion.String(),
				Resource:   "workspaces",
				Name:       workspace.Name,
				Cluster:    logicalcluster.From(workspace).String(),
				UID:        workspace.UID,
			},
		},
	}
	logicalClusterAdminClient, err := r.kcpLogicalClusterAdminClientFor(shard)
	if err != nil {
		return err
	}
	_, err = logicalClusterAdminClient.Cluster(cluster).TenancyV1alpha1().ThisWorkspaces().Create(ctx, this, metav1.CreateOptions{})
	return err
}

func (r *schedulingReconciler) createClusterRoleBindingForThisWorkspace(ctx context.Context, cluster logicalcluster.Name, shard *tenancyv1alpha1.ClusterWorkspaceShard, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	ownerAnnotation, found := workspace.Annotations[tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey]
	if !found {
		return fmt.Errorf("unable to find required %q owner annotation on %q workspace", tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey, workspace.Name)
	}

	var userInfo authenticationv1.UserInfo
	err := json.Unmarshal([]byte(ownerAnnotation), &userInfo)
	if err != nil {
		return fmt.Errorf("failed to unmarshal %q owner annotation with value %s on %q workspace", tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey, ownerAnnotation, workspace.Name)
	}
	newBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "workspace-admin",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     userInfo.Username,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}
	logicalClusterAdminClient, err := r.kubeLogicalClusterAdminClientFor(shard)
	if err != nil {
		return err
	}
	_, err = logicalClusterAdminClient.Cluster(cluster).RbacV1().ClusterRoleBindings().Create(ctx, newBinding, metav1.CreateOptions{})
	return err
}

// kcpLogicalClusterAdminClientFor returns a kcp client (i.e. a client that implements kcpclient.ClusterInterface) for the given shard.
// the returned client establishes a direct connection with the shard with credentials stored in r.logicalClusterAdminConfig.
// TODO:(p0lyn0mial): make it more efficient, maybe we need a per shard client pool or we could use an HTTPRoundTripper
func (r *schedulingReconciler) kcpLogicalClusterAdminClientFor(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclientset.ClusterInterface, error) {
	shardConfig := restclient.CopyConfig(r.logicalClusterAdminConfig)
	shardConfig.Host = shard.Spec.BaseURL
	shardClient, err := kcpclientset.NewForConfig(shardConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create shard %q kube client: %w", shard.Name, err)
	}
	return shardClient, nil
}

// kubeLogicalClusterAdminClientFor returns a kube client (i.e. a client that implements kubernetes.ClusterInterface) for the given shard.
// the returned client establishes a direct connection with the shard with credentials stored in r.logicalClusterAdminConfig.
// TODO:(p0lyn0mial): make it more efficient, maybe we need a per shard client pool or we could use an HTTPRoundTripper
func (r *schedulingReconciler) kubeLogicalClusterAdminClientFor(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kubernetes.ClusterInterface, error) {
	shardConfig := restclient.CopyConfig(r.logicalClusterAdminConfig)
	shardConfig.Host = shard.Spec.BaseURL
	shardClient, err := kubernetes.NewForConfig(shardConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create shard %q kube client: %w", shard.Name, err)
	}
	return shardClient, nil
}

func isValidShard(_ *tenancyv1alpha1.ClusterWorkspaceShard) (valid bool, reason, message string) {
	return true, "", ""
}

func hasThisWorkspaceFinalizer(workspace *tenancyv1alpha1.ClusterWorkspace) bool {
	for _, finalizer := range workspace.Finalizers {
		if finalizer == tenancyv1alpha1.ThisWorkspaceFinalizer {
			return true
		}
	}
	return false
}

func addThisWorkspaceFinalizer(workspace *tenancyv1alpha1.ClusterWorkspace) {
	workspace.Finalizers = append(workspace.Finalizers, tenancyv1alpha1.ThisWorkspaceFinalizer)
}

func addShardAnnotation(workspace *tenancyv1alpha1.ClusterWorkspace, shardName string) {
	if workspace.Annotations == nil {
		workspace.Annotations = map[string]string{}
	}
	workspace.Annotations[clusterWorkspaceShardAnnotationKey] = ByBase36Sha224NameValue(shardName)
}
