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
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	mathrand "math/rand"
	"net/url"
	"path"
	"strings"

	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

const (
	// WorkspaceShardHashAnnotationKey keeps track on which shard LogicalCluster must be scheduled. The value
	// is a base36(sha224) hash of the Shard name.
	WorkspaceShardHashAnnotationKey = "internal.tenancy.kcp.io/shard"

	// workspaceClusterAnnotationKey keeps track of the logical cluster on the shard.
	workspaceClusterAnnotationKey = "internal.tenancy.kcp.io/cluster"

	// unschedulableAnnotationKey is the annotation key used to indicate that a shard is unschedulable.
	// The annotation is meant to be used by e2e tests that otherwise started a private instance of kcp server.
	unschedulableAnnotationKey = "experimental.core.kcp.io/unschedulable"
)

type schedulingReconciler struct {
	generateClusterName func(path logicalcluster.Path) (logicalcluster.Name, error)

	getShard       func(name string) (*corev1alpha1.Shard, error)
	getShardByHash func(hash string) (*corev1alpha1.Shard, error)
	listShards     func(selector labels.Selector) ([]*corev1alpha1.Shard, error)

	getWorkspaceType func(clusterName logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)

	getLogicalCluster func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

	transitiveTypeResolver workspacetypeexists.TransitiveTypeResolver

	kcpLogicalClusterAdminClientFor  func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error)
	kubeLogicalClusterAdminClientFor func(shard *corev1alpha1.Shard) (kubernetes.ClusterInterface, error)
}

func (r *schedulingReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "scheduling")

	switch {
	case !workspace.DeletionTimestamp.IsZero():
		return reconcileStatusContinue, nil
	case workspace.Spec.URL != "" && workspace.Spec.Cluster != "":
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceScheduled)
		return reconcileStatusContinue, nil
	case workspace.Spec.URL == "" || workspace.Spec.Cluster == "":
		shardNameHash, hasShard := workspace.Annotations[WorkspaceShardHashAnnotationKey]
		clusterNameString, hasCluster := workspace.Annotations[workspaceClusterAnnotationKey]
		clusterName := logicalcluster.Name(clusterNameString)
		hasFinalizer := sets.New[string](workspace.Finalizers...).Has(corev1alpha1.LogicalClusterFinalizer)

		parentThis, err := r.getLogicalCluster(logicalcluster.From(workspace))
		if err != nil && !apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if apierrors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, nil // wait for parent LogicalCluster to be created
		}

		if !hasShard {
			shard, reason, err := r.chooseShardAndMarkCondition(logger, workspace) // call first with status side-effect, before any annotation aka spec change
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
			if shard == nil {
				conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonUnschedulable, conditionsv1alpha1.ConditionSeverityError, reason)
				return reconcileStatusContinue, nil // retry is automatic when new shards show up
			}
			logger.V(1).Info("Chose shard", "shard", shard.Name)
			shardNameHash = ByBase36Sha224NameValue(shard.Name)
			if workspace.Annotations == nil {
				workspace.Annotations = map[string]string{}
			}
			workspace.Annotations[WorkspaceShardHashAnnotationKey] = shardNameHash
			if region, found := shard.Labels["region"]; found {
				workspace.Labels["region"] = region
			}
		}
		if !hasCluster {
			cluster, err := r.generateClusterName(logicalcluster.From(workspace).Path().Join(workspace.Name))
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
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

		canonicalPath := logicalcluster.From(workspace).Path().Join(workspace.Name)
		if parentThis != nil {
			if parentPath := parentThis.Annotations[core.LogicalClusterPathAnnotationKey]; parentPath != "" {
				canonicalPath = logicalcluster.NewPath(parentThis.Annotations[core.LogicalClusterPathAnnotationKey]).Join(workspace.Name)
			}
		}

		if err := r.createLogicalCluster(ctx, shard, clusterName.Path(), canonicalPath, workspace); err != nil && !apierrors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		} else if apierrors.IsAlreadyExists(err) {
			// we have checked in createLogicalCluster that this is a logicalcluster from another owner. Let's choose another cluster name.
			delete(workspace.Annotations, workspaceClusterAnnotationKey)
			logging.WithObject(logger, shard).Info("logical cluster already exists")
			return reconcileStatusStopAndRequeue, nil
		}
		if err := r.updateLogicalClusterPhase(ctx, shard, clusterName.Path(), corev1alpha1.LogicalClusterPhaseInitializing); err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		// now complete the second part of our two-phase commit: set location in workspace
		u, err := url.Parse(shard.Spec.ExternalURL)
		if err != nil {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceScheduled, tenancyv1alpha1.WorkspaceReasonReasonUnknown, conditionsv1alpha1.ConditionSeverityError, "Invalid connection information on target Shard: %v.", err)
			return reconcileStatusStopAndRequeue, err // requeue
		}
		u.Path = path.Join(u.Path, canonicalPath.RequestPath())
		workspace.Spec.Cluster = clusterName.String()
		workspace.Spec.URL = u.String()
		logging.WithObject(logger, shard).Info("scheduled workspace to shard")
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}

func (r *schedulingReconciler) chooseShardAndMarkCondition(logger klog.Logger, workspace *tenancyv1alpha1.Workspace) (shard *corev1alpha1.Shard, reason string, err error) {
	selector := labels.Everything()
	if workspace.Spec.Location != nil {
		if workspace.Spec.Location.Selector != nil {
			var err error
			selector, err = metav1.LabelSelectorAsSelector(workspace.Spec.Location.Selector)
			if err != nil {
				return nil, fmt.Sprintf("spec.location.selector is invalid: %v", err), nil // don't retry, cannot do anything useful
			}
		}
	}

	shards, err := r.listShards(selector)
	if err != nil {
		return nil, "", err
	}

	validShards := make([]*corev1alpha1.Shard, 0, len(shards))
	invalidShards := map[string]struct {
		reason, message string
	}{}
	for _, shard := range shards {
		if _, ok := shard.Annotations[unschedulableAnnotationKey]; ok {
			logger.V(4).Info("Skipping a shard because it is annotated as unschedulable", "shard", shard.Name, "annotation", unschedulableAnnotationKey)
			continue
		}
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
		failures := make([]error, 0, len(invalidShards))
		for name, x := range invalidShards {
			failures = append(failures, fmt.Errorf("  %s: reason %q, message %q", name, x.reason, x.message))
		}
		logger.Error(utilerrors.NewAggregate(failures), "no valid shards found for workspace, skipping")
		return nil, "No available shards to schedule the workspace", nil // retry is automatic when new shards show up
	}
	targetShard := validShards[mathrand.Intn(len(validShards))]
	return targetShard, "", nil
}

func (r *schedulingReconciler) createLogicalCluster(ctx context.Context, shard *corev1alpha1.Shard, cluster logicalcluster.Path, canonicalPath logicalcluster.Path, workspace *tenancyv1alpha1.Workspace) error {
	logicalCluster := &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey: workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey],
				tenancyv1alpha1.LogicalClusterTypeAnnotationKey:         logicalcluster.NewPath(workspace.Spec.Type.Path).Join(string(workspace.Spec.Type.Name)).String(),
				core.LogicalClusterPathAnnotationKey:                    canonicalPath.String(),
			},
		},
		Spec: corev1alpha1.LogicalClusterSpec{
			Owner: &corev1alpha1.LogicalClusterOwner{
				APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
				Resource:   "workspaces",
				Name:       workspace.Name,
				Cluster:    logicalcluster.From(workspace).String(),
				UID:        workspace.UID,
			},
		},
	}
	if owner, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
		logicalCluster.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = owner
	}
	if groups, found := workspace.Annotations[authorization.RequiredGroupsAnnotationKey]; found {
		logicalCluster.Annotations[authorization.RequiredGroupsAnnotationKey] = groups
	}

	// add initializers
	var err error
	logicalCluster.Spec.Initializers, err = LogicalClustersInitializers(r.transitiveTypeResolver, r.getWorkspaceType, logicalcluster.NewPath(workspace.Spec.Type.Path), string(workspace.Spec.Type.Name))
	if err != nil {
		return err
	}

	logicalClusterAdminClient, err := r.kcpLogicalClusterAdminClientFor(shard)
	if err != nil {
		return err
	}
	logging.WithObject(klog.FromContext(ctx), logicalCluster).Info("creating LogicalCluster")
	_, err = logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Create(ctx, logicalCluster, metav1.CreateOptions{})

	if apierrors.IsAlreadyExists(err) {
		existing, getErr := logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		if equality.Semantic.DeepEqual(existing.Spec.Owner, logicalCluster.Spec.Owner) {
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
	wt, err := getWorkspaceType(typePath, typeName)
	if err != nil {
		return nil, err
	}
	wtAliases, err := resolver.Resolve(wt)
	if err != nil {
		return nil, err
	}

	initializers := make([]corev1alpha1.LogicalClusterInitializer, 0, len(wtAliases))

	bindings := false
	for _, alias := range wtAliases {
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
	logicalCluster, err := logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	logicalCluster.Status.Phase = phase
	logging.WithObject(klog.FromContext(ctx), logicalCluster).WithValues("phase", phase).Info("updating LogicalCluster phase")
	_, err = logicalClusterAdminClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().UpdateStatus(ctx, logicalCluster, metav1.UpdateOptions{})
	return err
}

func isValidShard(_ *corev1alpha1.Shard) (valid bool, reason, message string) {
	return true, "", ""
}

func randomClusterName(path logicalcluster.Path) (logicalcluster.Name, error) {
	token := make([]byte, 32)
	_, err := rand.Read(token)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum224(token)
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return logicalcluster.Name(base36hash[:16]), nil // 36^16 = 82 bits, P(conflict)<10^-9 for 2^26 clusters
}
