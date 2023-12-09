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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// workspaceStatusUpdater updates the status of the workspace based on the mount status.
// TODO(mjudeikis): For now this only updates the status of the mount annotation. In the future
// we should add second reconciler that will update the status of the workspace so triggering
// it to be "not ready" if the mount is not ready.
type workspaceStatusUpdater struct {
	getMountObject func(ctx context.Context, cluster logicalcluster.Path, ref *v1.ObjectReference) (*unstructured.Unstructured, error)
}

func (r *workspaceStatusUpdater) reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error) {
	var mount *tenancyv1alpha1.Mount
	if workspace.Annotations != nil {
		if v, ok := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey]; ok {
			var err error
			mount, err = tenancyv1alpha1.ParseTenancyMountAnnotation(v)
			if err != nil {
				return reconcileStatusStopAndRequeue, err
			}
		}
	}

	if mount == nil {
		// no mount annotation, nothing to do
		return reconcileStatusContinue, nil
	}

	switch {
	case !workspace.DeletionTimestamp.IsZero():
		return reconcileStatusContinue, nil
	case mount != nil && mount.MountSpec.Reference != nil:
		obj, err := r.getMountObject(ctx, logicalcluster.From(workspace).Path(), mount.MountSpec.Reference)
		if err != nil {
			return reconcileStatusStopAndRequeue, err
		}

		// we are working on status field. As this is "loose coupling, we parse it out"
		// Mount point implementors must expose status.{URL,phase} as a fields.
		// We are not interested in the rest of the status.
		status, ok := obj.Object["status"].(map[string]interface{})
		if !ok {
			return reconcileStatusStopAndRequeue, nil
		}
		statusPhase, ok := status["phase"].(string)
		if !ok {
			return reconcileStatusStopAndRequeue, nil
		}
		// url might not be there if the mount is not ready
		statusURL, ok := status["URL"].(string)
		if !ok {
			statusURL = ""
		}
		mount.MountStatus.Phase = tenancyv1alpha1.MountPhaseType(statusPhase)
		mount.MountStatus.URL = statusURL

		workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey] = mount.String()
	}

	return reconcileStatusContinue, nil
}
