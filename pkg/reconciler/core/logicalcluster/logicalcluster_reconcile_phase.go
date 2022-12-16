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

package logicalcluster

import (
	"context"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

type phaseReconciler struct{}

func (r *phaseReconciler) reconcile(ctx context.Context, workspace *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	switch workspace.Status.Phase {
	case corev1alpha1.LogicalClusterPhaseInitializing:
		if len(workspace.Status.Initializers) > 0 {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedInitializerExists, conditionsv1alpha1.ConditionSeverityInfo, "Initializers still exist: %v", workspace.Status.Initializers)
			return reconcileStatusContinue, nil
		}

		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceInitialized)
	}

	return reconcileStatusContinue, nil
}
