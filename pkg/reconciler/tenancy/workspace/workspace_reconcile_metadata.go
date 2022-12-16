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
	"encoding/json"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacedeletion/deletion"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(ctx context.Context, workspace *tenancyv1beta1.Workspace) (reconcileStatus, error) {
	logger := klog.FromContext(ctx).WithValues("reconciler", "metadata")

	changed := false
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

	// remote deletion finalizer as this moved to the LogicalCluster
	if workspace.DeletionTimestamp.IsZero() {
		finalizers := sets.NewString(workspace.Finalizers...)
		if finalizers.Has(deletion.WorkspaceFinalizer) {
			workspace.Finalizers = finalizers.Delete(deletion.WorkspaceFinalizer).List()
			changed = true
		}
	}

	if workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseReady {
		if value, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
			var info authenticationv1.UserInfo
			err := json.Unmarshal([]byte(value), &info)
			if err != nil {
				logger.Error(err, "failed to unmarshal workspace owner annotation", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, value)
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if userOnlyValue, err := json.Marshal(authenticationv1.UserInfo{Username: info.Username}); err != nil {
				// should never happen
				logger.Error(err, "failed to marshal user info")
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if value != string(userOnlyValue) {
				workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = string(userOnlyValue)
				changed = true
			}
		}
	}

	if changed {
		// first update ObjectMeta before status
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}
