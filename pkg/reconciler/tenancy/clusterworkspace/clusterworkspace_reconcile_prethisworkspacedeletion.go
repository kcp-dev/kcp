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

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type preThisWorkspaceReconciler struct {
}

func (r *preThisWorkspaceReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	finalizers := sets.NewString(workspace.Finalizers...)
	if !workspace.DeletionTimestamp.IsZero() && finalizers.Has(tenancyv1alpha1.ThisWorkspaceFinalizer) {
		logger.Info("Deleting finalizer", "name", tenancyv1alpha1.ThisWorkspaceFinalizer)
		workspace.Finalizers = finalizers.Delete(tenancyv1alpha1.ThisWorkspaceFinalizer).List()
		return reconcileStatusStopAndRequeue, nil // spec change
	}

	return reconcileStatusContinue, nil
}
