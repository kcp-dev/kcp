/*
Copyright 2021 The kcp Authors.

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

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(_ context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	if workspace.Spec.Mount != nil {
		return reconcileStatusContinue, nil
	}

	changed := false

	if workspace.Status.Phase == "" {
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseScheduling
		changed = true
	}

	expected := string(workspace.Status.Phase)
	if !workspace.DeletionTimestamp.IsZero() {
		expected = "Deleting"
	}
	if got := workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel]; got != expected {
		if workspace.Labels == nil {
			workspace.Labels = map[string]string{}
		}
		workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel] = expected
		changed = true
	}

	// Note: group-wiping of the owner annotation on Ready phase was removed.
	// With spec.ownerUser on LogicalCluster as the structured, immutable source of truth,
	// there is no longer a reason to strip data from the annotation.

	if changed {
		// first update ObjectMeta before status
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}
