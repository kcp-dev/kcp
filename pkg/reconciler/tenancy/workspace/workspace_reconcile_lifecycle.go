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
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

type LogicalClusterResource = committer.Resource[*corev1alpha1.LogicalClusterSpec, *corev1alpha1.LogicalClusterStatus]

type lifecycleReconciler struct {
	getLogicalCluster func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)

	commit func(ctx context.Context, old, new *LogicalClusterResource) error

	requeueAfter func(workspace *tenancyv1alpha1.Workspace, after time.Duration)
}

func (r *lifecycleReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "lifecycle")

	if !workspace.DeletionTimestamp.IsZero() {
		logger.Info("workspace is deleting")
		return reconcileStatusContinue, nil
	}

	if workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		return reconcileStatusContinue, nil
	}

	logger = logger.WithValues("cluster", workspace.Spec.Cluster)

	logicalCluster, err := r.getLogicalCluster(ctx, logicalcluster.NewPath(workspace.Spec.Cluster))
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, err
	} else if apierrors.IsNotFound(err) {
		logger.Info("LogicalCluster disappeared")
		return reconcileStatusContinue, nil
	}

	if value, found := logicalCluster.Annotations[tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey]; !found || value != workspace.Spec.DefaultAPIBindingLifecycle {
		newMeta := logicalCluster.ObjectMeta.DeepCopy()
		newMeta.Annotations[tenancyv1alpha1.ExperimentalDefaultAPIBindingLifecycleAnnotationKey] = workspace.Spec.DefaultAPIBindingLifecycle

		old := &LogicalClusterResource{ObjectMeta: logicalCluster.ObjectMeta, Spec: &logicalCluster.Spec, Status: &logicalCluster.Status}
		new := &LogicalClusterResource{ObjectMeta: *newMeta, Spec: &logicalCluster.Spec, Status: &logicalCluster.Status}
		return reconcileStatusContinue, r.commit(ctx, old, new)
	}

	return reconcileStatusContinue, nil
}