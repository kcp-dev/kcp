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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"path"
	"strings"

	kubernetes "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacedeletion/deletion"
)

const (
	// workspaceShardAnnotationKey keeps track on which shard LogicalCluster must be scheduled. The value
	// is a base36(sha224) hash of the Shard name.
	workspaceShardAnnotationKey = "internal.tenancy.kcp.dev/shard"
	// workspaceClusterAnnotationKey keeps track of the logical cluster on the shard.
	workspaceClusterAnnotationKey = "internal.tenancy.kcp.dev/cluster"
)

type schedulingReconciler struct {
	generateClusterName func(path logicalcluster.Path) logicalcluster.Name

	getShard       func(name string) (*corev1alpha1.Shard, error)
	getShardByHash func(hash string) (*corev1alpha1.Shard, error)
	listShards     func(selector labels.Selector) ([]*corev1alpha1.Shard, error)

	getWorkspaceType func(clusterName logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)

	getLogicalCluster func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

	transitiveTypeResolver workspacetypeexists.TransitiveTypeResolver

	kcpLogicalClusterAdminClientFor  func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error)
	kubeLogicalClusterAdminClientFor func(shard *corev1alpha1.Shard) (kubernetes.ClusterInterface, error)
}

func (r *schedulingReconciler) reconcile(ctx context.Context, workspace *tenancyv1beta1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "scheduling")

	switch workspace.Status.Phase {
	case corev1alpha1.LogicalClusterPhaseScheduling:
		shardNameHash, hasShard := workspace.Annotations[workspaceShardAnnotationKey]
		clusterNameString, hasCluster := workspace.Annotations[workspaceClusterAnnotationKey]
		clusterName := logicalcluster.Name(clusterNameString)
		hasFinalizer := sets.NewString(workspace.Finalizers...).Has(corev1alpha1.LogicalClusterFinalizer)

		parentThis, err := r.getLogicalCluster(logicalcluster.From(workspace))
		if err != nil && !apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, nil // wait for parent LogicalCluster to be created
		}

		if !hasShard {
			shardName, err := r.chooseShardAndMarkCondition(logger, workspace) // call first with status side-effect, before any annotation aka spec change
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
			if len(shardName) == 0 {
				return reconcileStatusContinue, nil // retry is automatic when new shards show up
			}
			shardNameHash = ByBase36Sha224NameValue(shardName)
			if workspace.Annotations == nil {
				workspace.Annotations = map[string]string{}
			}
			workspace.Annotations[workspaceShardAnnotationKey] = shardNameHash
		}
		if !hasCluster {
			cluster := r.generateClusterName(logicalcluster.From(workspace).Path().Join(workspace.Name))
			if workspace.Annotations == nil {
				workspace.Annotations = map[string]string{}
			}
			workspace.Annotations[workspaceClusterAnnotationKey] = cluster.String()
		}
		if !hasFinalizer {
			workspace.Finalizers = append(workspace.Finalizers, corev1alpha1.LogicalClusterFinalizer)
		}
		if !hasShard || !hasCluster || !hasFinalizer {
			// this is the first part of our two-phase commit
			// the first phase is to pick up a shard
			return reconcileStatusStopAndRequeue, nil
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

		if err := r.createLogicalCluster(ctx, shard, clusterName.Path(), parentThis, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		} else if apierrors.IsAlreadyExists(err) {
			// we have checked in createLogicalCluster that this is a logicalcluster from another owner. Let's choose another cluster name.
			delete(workspace.Annotations, workspaceClusterAnnotationKey)
			return reconcileStatusStopAndRequeue, nil
		}
		if err := r.createClusterRoleBindingForLogicalCluster(ctx, shard, clusterName.Path(), workspace); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		}
		if err := r.updateLogicalClusterPhase(ctx, shard, clusterName.Path(), corev1alpha1.LogicalClusterPhaseInitializing); err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		// now complete the second part of our two-phase commit: set location in workspace
		u, err := url.Parse(shard.Spec.ExternalURL)
		if err != nil {
			// shouldn't happen since we just checked in isValidShard
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonReasonUnknown, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target Shard: %v.", err)
			return reconcileStatusStopAndRequeue, err // requeue
		}

		u.Path = path.Join(u.Path, clusterName.Path().RequestPath())
		workspace.Status.URL = u.String()
		workspace.Status.Cluster = clusterName.String()
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
		logging.WithObject(logger, shard).Info("scheduled workspace to shard")
	}

	return reconcileStatusContinue, nil
}

func (r *schedulingReconciler) chooseShardAndMarkCondition(logger klog.Logger, workspace *tenancyv1beta1.Workspace) (string, error) {
	selector := labels.Everything()
	var shards []*corev1alpha1.Shard
	if workspace.Spec.Location != nil {
		if workspace.Spec.Location.Selector != nil {
			var err error
			selector, err = metav1.LabelSelectorAsSelector(workspace.Spec.Location.Selector)
			if err != nil {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, "spec.location.selector is invalid: %v", err)
				return "", nil // don't retry, cannot do anything useful
			}
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
		if len(shards) > 0 && (workspace.Spec.Location == nil || workspace.Spec.Location.Selector == nil) {
			// trim the list to contain only the "root" shard so that we always schedule onto it
			for _, shard := range shards {
				if shard.Name == "root" {
					shards = []*corev1alpha1.Shard{shard}
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

	validShards := make([]*corev1alpha1.Shard, 0, len(shards))
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

func (r *schedulingReconciler) createLogicalCluster(ctx context.Context, shard *corev1alpha1.Shard, cluster logicalcluster.Path, parent *corev1alpha1.LogicalCluster, workspace *tenancyv1beta1.Workspace) error {
	canonicalPath := logicalcluster.From(workspace).Path().Join(workspace.Name)
	if parent != nil {
		if parentPath := parent.Annotations[core.LogicalClusterPathAnnotationKey]; parentPath != "" {
			canonicalPath = logicalcluster.NewPath(parent.Annotations[core.LogicalClusterPathAnnotationKey]).Join(workspace.Name)
		}
	}
	this := &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       corev1alpha1.LogicalClusterName,
			Finalizers: []string{deletion.WorkspaceFinalizer},
			Annotations: map[string]string{
				tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey: workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey],
				tenancyv1beta1.LogicalClusterTypeAnnotationKey:          logicalcluster.NewPath(workspace.Spec.Type.Path).Join(string(workspace.Spec.Type.Name)).String(),
				core.LogicalClusterPathAnnotationKey:                    canonicalPath.String(),
			},
		},
		Spec: corev1alpha1.LogicalClusterSpec{
			Owner: &corev1alpha1.LogicalClusterOwner{
				APIVersion: tenancyv1beta1.SchemeGroupVersion.String(),
				Resource:   "workspaces",
				Name:       workspace.Name,
				Cluster:    logicalcluster.From(workspace).String(),
				UID:        workspace.UID,
			},
		},
	}
	if owner, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
		this.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = owner
	}
	if groups, found := workspace.Annotations[authorization.RequiredGroupsAnnotationKey]; found {
		this.Annotations[authorization.RequiredGroupsAnnotationKey] = groups
	}

	// add initializers
	var err error
	this.Spec.Initializers, err = LogicalClustersInitializers(r.transitiveTypeResolver, r.getWorkspaceType, logicalcluster.NewPath(workspace.Spec.Type.Path), string(workspace.Spec.Type.Name))
	if err != nil {
		return err
	}

	logicalClusterAdminClient, err := r.kcpLogicalClusterAdminClientFor(shard)
	if err != nil {
		return err
	}
	_, err = logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Create(ctx, this, metav1.CreateOptions{})

	if apierrors.IsAlreadyExists(err) {
		existing, getErr := logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if equality.Semantic.DeepEqual(existing.Spec.Owner, this.Spec.Owner) {
			return nil
		}
	}

	return err
}

// LogicalClustersInitializers returns the initializers for a LogicalCluster of a given
// fully-qualified WorkspaceType reference.
func LogicalClustersInitializers(
	resolver workspacetypeexists.TransitiveTypeResolver,
	getWorkspaceType func(clusterName logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error),
	typePath logicalcluster.Path, typeName string,
) ([]corev1alpha1.LogicalClusterInitializer, error) {
	cwt, err := getWorkspaceType(typePath, typeName)
	if err != nil {
		return nil, err
	}
	cwtAliases, err := resolver.Resolve(cwt)
	if err != nil {
		return nil, err
	}

	initializers := make([]corev1alpha1.LogicalClusterInitializer, 0, len(cwtAliases))

	bindings := false
	for _, alias := range cwtAliases {
		if alias.Spec.Initializer {
			initializers = append(initializers, initialization.InitializerForType(alias))
		}
		bindings = bindings || len(alias.Spec.DefaultAPIBindings) > 0
	}
	if bindings {
		initializers = append(initializers, tenancyv1alpha1.WorkspaceAPIBindingsInitializer)
	}

	return initializers, nil
}

func (r *schedulingReconciler) updateLogicalClusterPhase(ctx context.Context, shard *corev1alpha1.Shard, cluster logicalcluster.Path, phase corev1alpha1.LogicalClusterPhaseType) error {
	logicalClusterAdminClient, err := r.kcpLogicalClusterAdminClientFor(shard)
	if err != nil {
		return err
	}
	this, err := logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	this.Status.Phase = phase
	_, err = logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().UpdateStatus(ctx, this, metav1.UpdateOptions{})
	return err
}

func (r *schedulingReconciler) createClusterRoleBindingForLogicalCluster(ctx context.Context, shard *corev1alpha1.Shard, cluster logicalcluster.Path, workspace *tenancyv1beta1.Workspace) error {
	ownerAnnotation, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]
	if !found {
		return fmt.Errorf("unable to find required %q owner annotation on %q workspace", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, workspace.Name)
	}

	var userInfo authenticationv1.UserInfo
	err := json.Unmarshal([]byte(ownerAnnotation), &userInfo)
	if err != nil {
		return fmt.Errorf("failed to unmarshal %q owner annotation with value %s on %q workspace", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, ownerAnnotation, workspace.Name)
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

func isValidShard(_ *corev1alpha1.Shard) (valid bool, reason, message string) {
	return true, "", ""
}

func randomClusterName(path logicalcluster.Path) logicalcluster.Name {
	token := make([]byte, 32)
	rand.Read(token)
	hash := sha256.Sum224(token)
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return logicalcluster.Name(base36hash[:16]) // 36^16 = 82 bits, P(conflict)<10^-9 for 2^26 clusters
}
