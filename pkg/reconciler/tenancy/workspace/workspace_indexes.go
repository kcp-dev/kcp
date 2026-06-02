/*
Copyright 2022 The kcp Authors.

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
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

const (
	unschedulable    = "unschedulable"
	byLogicalCluster = "byLogicalCluster"
)

func indexUnschedulable(obj interface{}) ([]string, error) {
	workspace := obj.(*tenancyv1alpha1.Workspace)
	if conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && conditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable {
		return []string{"true"}, nil
	}
	return []string{}, nil
}

// indexWorkspaceByLogicalCluster indexes Workspaces by their spec.cluster (the
// logical cluster name of the underlying LogicalCluster). This lets the
// controller quickly find the Workspace tied to a given LogicalCluster when a
// LogicalCluster event fires.
func indexWorkspaceByLogicalCluster(obj interface{}) ([]string, error) {
	workspace := obj.(*tenancyv1alpha1.Workspace)
	if workspace.Spec.Cluster == "" {
		return []string{}, nil
	}
	return []string{workspace.Spec.Cluster}, nil
}
