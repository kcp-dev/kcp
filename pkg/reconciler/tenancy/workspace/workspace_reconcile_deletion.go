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

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

type deletionReconciler struct {
	getLogicalCluster    func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)
	deleteLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) error
}

func (r *deletionReconciler) reconcile(ctx context.Context, workspace *tenancyv1beta1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "deletion")
	logger = logger.WithValues("cluster", workspace.Status.Cluster)

	if workspace.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	if sets.NewString(workspace.Finalizers...).Delete(corev1alpha1.LogicalClusterFinalizer).Len() > 0 {
		return reconcileStatusContinue, nil
	}

	// If the logical cluster hasn't been created yet, we have to look at the annotation to
	// get the cluster name, instead of looking at status.
	clusterName := logicalcluster.Name(workspace.Status.Cluster)
	if workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseScheduling {
		a, ok := workspace.Annotations[workspaceClusterAnnotationKey]
		if !ok {
			finalizers := sets.NewString(workspace.Finalizers...)
			finalizers.Delete(corev1alpha1.LogicalClusterFinalizer)
			workspace.Finalizers = finalizers.List()
			return reconcileStatusContinue, nil
		}

		clusterName = logicalcluster.Name(a)
	}

	if _, err := r.getLogicalCluster(ctx, clusterName.Path()); err != nil && !apierrors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, err
	} else if apierrors.IsNotFound(err) {
		finalizers := sets.NewString(workspace.Finalizers...)
		if finalizers.Has(corev1alpha1.LogicalClusterFinalizer) {
			logger.Info(fmt.Sprintf("Removing finalizer %s", corev1alpha1.LogicalClusterFinalizer))
			workspace.Finalizers = finalizers.Delete(corev1alpha1.LogicalClusterFinalizer).List()
			return reconcileStatusStopAndRequeue, nil // spec change
		}
		return reconcileStatusContinue, nil
	}

	logger.Info("Deleting LogicalCluster")
	if err := r.deleteLogicalCluster(ctx, clusterName.Path()); err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	// here we are waiting for the other shard to remove the finalizer of the Workspace

	return reconcileStatusContinue, nil
}
