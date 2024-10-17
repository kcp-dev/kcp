/*
Copyright 2024 The KCP Authors.

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

package workspacestatuses

import (
	"context"

	v1 "k8s.io/api/core/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// workspaceStatusUpdater updates the status of the workspace based on the conditions.
type workspacePhaseUpdater struct{}

func (r *workspacePhaseUpdater) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	for _, c := range workspace.Status.Conditions {
		if c.Status != v1.ConditionTrue {
			workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseNotReady
			return reconcileStatusContinue, nil
		}
	}

	workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
	return reconcileStatusContinue, nil
}
