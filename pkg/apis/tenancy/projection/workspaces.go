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

func ProjectWorkspaceToClusterWorkspace(from *tenancyv1beta1.Workspace, to *tenancyv1alpha1.ClusterWorkspace) {
	to.ObjectMeta = from.ObjectMeta
	to.Spec.Type = tenancyv1alpha1.WorkspaceTypesReference{
		Path: from.Spec.Type.Path,
		Name: from.Spec.Type.Name,
	}
	if from.Spec.Location != nil {
		to.Spec.Shard = &tenancyv1alpha1.ShardConstraints{
			Selector: from.Spec.Location.Selector,
		}
	}
	to.Status.BaseURL = from.Status.URL
	to.Status.Phase = from.Status.Phase
	to.Status.Initializers = from.Status.Initializers
	to.Status.Cluster = from.Status.Cluster
	to.Status.Conditions = from.Status.Conditions
}
