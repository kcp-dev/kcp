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

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

type deletionReconciler struct {
	getThisWorkspace    func(ctx context.Context, cluster logicalcluster.Path) (*tenancyv1alpha1.ThisWorkspace, error)
	deleteThisWorkspace func(ctx context.Context, cluster logicalcluster.Path) error
}

func (r *deletionReconciler) reconcile(ctx context.Context, workspace *tenancyv1beta1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "deletion")
	logger = logger.WithValues("cluster", workspace.Status.Cluster)

	if workspace.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	if sets.NewString(workspace.Finalizers...).Delete(tenancyv1alpha1.ThisWorkspaceFinalizer).Len() > 0 {
		return reconcileStatusContinue, nil
	}

	if _, err := r.getThisWorkspace(ctx, logicalcluster.New(workspace.Status.Cluster)); err != nil && !apierrors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, err
	} else if apierrors.IsNotFound(err) {
		finalizers := sets.NewString(workspace.Finalizers...)
		if finalizers.Has(tenancyv1alpha1.ThisWorkspaceFinalizer) {
			logger.Info(fmt.Sprintf("Removing finalizer %s", tenancyv1alpha1.ThisWorkspaceFinalizer))
			workspace.Finalizers = finalizers.Delete(tenancyv1alpha1.ThisWorkspaceFinalizer).List()
			return reconcileStatusStopAndRequeue, nil // spec change
		}
		return reconcileStatusContinue, nil
	}

	logger.Info("Deleting ThisWorkspace")
	if err := r.deleteThisWorkspace(ctx, logicalcluster.New(workspace.Status.Cluster)); err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	// here we are waiting for the other shard to remove the finalizer of the Workspace

	return reconcileStatusContinue, nil
}
