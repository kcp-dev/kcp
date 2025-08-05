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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

type deletionReconciler struct {
	getLogicalCluster    func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
	deleteLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) error

	getShardByHash func(hash string) (*corev1alpha1.Shard, error)

	kcpLogicalClusterAdminClientFor func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error)
}

func (r *deletionReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "deletion")
	logger = logger.WithValues("cluster", workspace.Spec.Cluster)

	if workspace.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	finSet := sets.New(workspace.Finalizers...)

	// we want our finalizer to be removed last, so check if other finalizers exist
	if finSet.Has(corev1alpha1.LogicalClusterFinalizer) && finSet.Len() > 1 {
		return reconcileStatusContinue, nil
	}

	// If the logical cluster hasn't been created yet, we have to look at the annotation to
	// get the cluster name, instead of looking at status.
	clusterName := logicalcluster.Name(workspace.Spec.Cluster)
	if workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseScheduling {
		a, ok := workspace.Annotations[workspaceClusterAnnotationKey]
		// if the logicalcluster was never created, we can directly remove the
		// workspace finalizer
		if !ok {
			workspace.Finalizers = sets.List(finSet.Delete(corev1alpha1.LogicalClusterFinalizer))
			return reconcileStatusContinue, nil
		}

		clusterName = logicalcluster.Name(a)
	}

	logicalCluster, getErr := r.getLogicalCluster(ctx, clusterName.Path())
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		// try again with a direct connection. It might be that the front-proxy
		// does not know about the logical cluster. We don't want to leak, so
		// try extra hard.
		shardNameHash, hasShard := workspace.Annotations[WorkspaceShardHashAnnotationKey]
		if !hasShard {
			// nothing we can do beyond retrying
			return reconcileStatusStopAndRequeue, getErr
		}

		shard, err := r.getShardByHash(shardNameHash)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		logicalClusterAdminClient, err := r.kcpLogicalClusterAdminClientFor(shard)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		logicalCluster, getErr = logicalClusterAdminClient.Cluster(clusterName.Path()).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return reconcileStatusStopAndRequeue, getErr
		}

		// fall-through
	}
	if apierrors.IsNotFound(getErr) {
		if finSet.Has(corev1alpha1.LogicalClusterFinalizer) {
			logger.Info(fmt.Sprintf("Removing finalizer %s", corev1alpha1.LogicalClusterFinalizer))
			workspace.Finalizers = sets.List(finSet.Delete(corev1alpha1.LogicalClusterFinalizer))
			return reconcileStatusStopAndRequeue, nil // spec change
		}
		return reconcileStatusContinue, nil
	}

	if logicalCluster.DeletionTimestamp.IsZero() {
		logger.Info("Deleting LogicalCluster")
		if err := r.deleteLogicalCluster(ctx, clusterName.Path()); err != nil {
			return reconcileStatusStopAndRequeue, err
		}
	}

	// here we are waiting for the other shard to remove the finalizer of the Workspace

	return reconcileStatusContinue, nil
}
