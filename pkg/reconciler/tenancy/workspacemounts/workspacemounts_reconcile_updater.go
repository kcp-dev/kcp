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

package workspacemounts

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// workspaceStatusUpdater updates the status of the workspace based on the mount status.
// TODO(mjudeikis): For now this only updates the status of the mount annotation. In the future
// we should add second reconciler that will update the status of the workspace so triggering
// it to be "not ready" if the mount is not ready.
type workspaceStatusUpdater struct {
	getMountObject func(ctx context.Context, cluster logicalcluster.Path, ref *tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error)
}

func (r *workspaceStatusUpdater) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	v, ok := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey]
	if !ok {
		// no mount annotation, might be nothing or mount was soft "deleted" by removing the annotation.
		// Delete the condition.
		conditions.Delete(workspace, tenancyv1alpha1.MountConditionReady)
		return reconcileStatusContinue, nil
	}
	mount, err := tenancyv1alpha1.ParseTenancyMountAnnotation(v)
	if err != nil {
		conditions.MarkFalse(
			workspace,
			tenancyv1alpha1.MountConditionReady,
			tenancyv1alpha1.MountAnnotationInvalidReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Annotation %q is invalid: %v", tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey, err,
		)
		return reconcileStatusContinue, nil
	} else if mount == nil {
		conditions.MarkFalse(
			workspace,
			tenancyv1alpha1.MountConditionReady,
			tenancyv1alpha1.MountAnnotationInvalidReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Annotation %q is invalid: no mount JSON", tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey,
		)
		return reconcileStatusContinue, nil
	}

	switch {
	case !workspace.DeletionTimestamp.IsZero():
		return reconcileStatusContinue, nil
	case mount != nil && mount.MountSpec.Reference != nil:
		obj, err := r.getMountObject(ctx, logicalcluster.From(workspace).Path(), mount.MountSpec.Reference)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		} else if kerrors.IsNotFound(err) {
			conditions.MarkFalse(
				workspace,
				tenancyv1alpha1.MountConditionReady,
				tenancyv1alpha1.MountObjectNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"%s %q not found",
				obj.GroupVersionKind().Kind, types.NamespacedName{Namespace: obj.GetNamespace(), Name: obj.GetName()},
			)
			return reconcileStatusContinue, nil
		}

		// we are working on status field. As this is "loose coupling, we parse it out"
		// Mount point implementors must expose status.{URL,phase} as a fields.
		// We are not interested in the rest of the status.
		statusPhase, ok, err := unstructured.NestedString(obj.Object, "status", "phase")
		if err != nil || !ok {
			conditions.MarkFalse(
				workspace,
				tenancyv1alpha1.MountConditionReady,
				tenancyv1alpha1.MountObjectNotReadyReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Mount is not reporting ready. See %s %q status for more details",
				obj.GroupVersionKind().Kind, obj.GetName(),
			)

			return reconcileStatusContinue, nil //nolint:nilerr // we ignore the error intentionally. Not helpful.
		}

		// url might not be there if the mount is not ready
		statusURL, _, _ := unstructured.NestedString(obj.Object, "status", "URL")

		// Only Spec or Status can be updated, not both.
		if mount.MountStatus.Phase != tenancyv1alpha1.MountPhaseType(statusPhase) || mount.MountStatus.URL != statusURL {
			mount.MountStatus.Phase = tenancyv1alpha1.MountPhaseType(statusPhase)
			mount.MountStatus.URL = statusURL

			workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey] = mount.String()

			return reconcileStatusStopAndRequeue, nil
		}

		// Inject condition into the workspace.
		// This is a loose coupling, we are not interested in the rest of the status.
		switch mount.MountStatus.Phase {
		case tenancyv1alpha1.MountPhaseReady:
			conditions.MarkTrue(workspace, tenancyv1alpha1.MountConditionReady)
		default:
			conditions.MarkFalse(
				workspace,
				tenancyv1alpha1.MountConditionReady,
				tenancyv1alpha1.MountObjectNotReadyReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Mount is not reporting ready. See %s %q status for more details",
				obj.GroupVersionKind().Kind, obj.GetName(),
			)
		}
	}

	return reconcileStatusContinue, nil
}
