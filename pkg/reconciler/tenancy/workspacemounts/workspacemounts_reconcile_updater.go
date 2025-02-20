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
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// workspaceStatusUpdater updates the status of the workspace based on the mount status.
// TODO(mjudeikis): For now this only updates the status of the mount annotation. In the future
// we should add second reconciler that will update the status of the workspace so triggering
// it to be "not ready" if the mount is not ready.
type workspaceStatusUpdater struct {
	getMountObject func(ctx context.Context, cluster logicalcluster.Path, ref tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error)
}

func (r *workspaceStatusUpdater) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	if workspace.Spec.Mount == nil {
		return reconcileStatusContinue, nil
	}

	if !workspace.DeletionTimestamp.IsZero() {
		return reconcileStatusContinue, nil
	}

	obj, err := r.getMountObject(ctx, logicalcluster.From(workspace).Path(), workspace.Spec.Mount.Reference)
	if err != nil {
		if kerrors.IsNotFound(err) {
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
		return reconcileStatusStopAndRequeue, err
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
	if err := fillWorkspaceSpec(obj, workspace); err != nil {
		return reconcileStatusStopAndRequeue, err
	}
	// Inject condition into the workspace.
	// This is a loose coupling, we are not interested in the rest of the status.
	switch tenancyv1alpha1.MountPhaseType(statusPhase) {
	case tenancyv1alpha1.MountPhaseReady:
		conditions.MarkTrue(workspace, tenancyv1alpha1.MountConditionReady)
		workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
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

	return reconcileStatusContinue, nil
}

func fillWorkspaceSpec(obj *unstructured.Unstructured, workspace *tenancyv1alpha1.Workspace) error {
	// url might not be there if the mount is not ready
	statusURL, found, err := unstructured.NestedString(obj.Object, "status", "URL")
	if !found || err != nil {
		return fmt.Errorf("unable to read .status.URL, found %v, err: %w", found, err)
	}
	workspace.Spec.URL = statusURL

	clusterName, _, _ := unstructured.NestedString(obj.Object, "status", "cluster")
	workspace.Spec.Cluster = clusterName

	wsTypeName, found, err := unstructured.NestedString(obj.Object, "status", "type", "name")
	if !found || err != nil {
		return fmt.Errorf("unable to read .status.type.name, found %v, err: %w", found, err)
	}

	if workspace.Spec.Type == nil {
		workspace.Spec.Type = &tenancyv1alpha1.WorkspaceTypeReference{}
	}
	workspace.Spec.Type.Name = tenancyv1alpha1.WorkspaceTypeName(wsTypeName)

	wsTypePath, found, err := unstructured.NestedString(obj.Object, "status", "type", "path")
	if !found || err != nil {
		return fmt.Errorf("unable to read .status.type.path, found %v, err: %w", found, err)
	}
	workspace.Spec.Type.Path = wsTypePath

	return nil
}
