/*
Copyright 2022 The KCP Authors.

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

package projection

import (
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

func ProjectClusterWorkspaceToWorkspace(from *tenancyv1alpha1.ClusterWorkspace, to *tenancyv1beta1.Workspace) {
	to.ObjectMeta = from.ObjectMeta
	to.Spec.Type = from.Spec.Type
	to.Status.URL = from.Status.BaseURL
	to.Status.Phase = from.Status.Phase
	to.Status.Initializers = from.Status.Initializers
	to.Status.Cluster = from.Status.Cluster

	to.Annotations = make(map[string]string, len(from.Annotations))
	for k, v := range from.Annotations {
		if k == tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey {
			// do not leak user information
			continue
		}
		to.Annotations[k] = v
	}

	for i := range from.Status.Conditions {
		c := &from.Status.Conditions[i]
		switch c.Type {
		case tenancyv1alpha1.WorkspaceContentDeleted,
			tenancyv1alpha1.WorkspaceDeletionContentSuccess,
			tenancyv1alpha1.WorkspaceInitialized,
			tenancyv1alpha1.WorkspaceAPIBindingsInitialized:
			to.Status.Conditions = append(to.Status.Conditions, *c)
		}
	}
}
