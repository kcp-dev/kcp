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

package v1alpha1

import (
	"fmt"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// WorkspaceTypeReference is a globally unique, fully qualified reference to a
// cluster workspace type.
type WorkspaceTypeReference struct {
	// name is the name of the WorkspaceType
	//
	// +required
	// +kubebuilder:validation:Required
	Name WorkspaceTypeName `json:"name"`

	// path is an absolute reference to the workspace that owns this type, e.g. root:org:ws.
	//
	// +optional
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`
}

// WorkspaceTypeName is a name of a WorkspaceType
//
// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?`
type WorkspaceTypeName string

func (r WorkspaceTypeReference) String() string {
	if r.Path == "" {
		return string(r.Name)
	}
	return fmt.Sprintf("%s:%s", r.Path, r.Name)
}

const ExperimentalWorkspaceOwnerAnnotationKey string = "experimental.tenancy.kcp.io/owner"

// These are valid conditions of workspace.
const (
	// WorkspaceScheduled represents status of the scheduling process for this workspace.
	WorkspaceScheduled conditionsv1alpha1.ConditionType = "WorkspaceScheduled"
	// WorkspaceReasonUnschedulable reason in WorkspaceScheduled WorkspaceCondition means that the scheduler
	// can't schedule the workspace right now, for example due to insufficient resources in the cluster.
	WorkspaceReasonUnschedulable = "Unschedulable"
	// WorkspaceReasonReasonUnknown reason in WorkspaceScheduled means that scheduler has failed for
	// some unexpected reason.
	WorkspaceReasonReasonUnknown = "Unknown"

	// WorkspaceContentDeleted represents the status that all resources in the workspace are deleted.
	WorkspaceContentDeleted conditionsv1alpha1.ConditionType = "WorkspaceContentDeleted"

	// WorkspaceInitialized represents the status that initialization has finished.
	WorkspaceInitialized conditionsv1alpha1.ConditionType = "WorkspaceInitialized"
	// WorkspaceInitializedInitializerExists reason in WorkspaceInitialized condition means that there is at least
	// one initializer still left.
	WorkspaceInitializedInitializerExists = "InitializerExists"
	// WorkspaceInitializedWorkspaceDisappeared reason in WorkspaceInitialized condition means that the LogicalCluster
	// object has disappeared.
	WorkspaceInitializedWorkspaceDisappeared = "WorkspaceDisappeared"

	// WorkspaceAPIBindingsInitialized represents the status of the initial APIBindings for the workspace.
	WorkspaceAPIBindingsInitialized conditionsv1alpha1.ConditionType = "APIBindingsInitialized"
	// WorkspaceInitializedWaitingOnAPIBindings is a reason for the APIBindingsInitialized condition that indicates
	// at least one APIBinding is not ready.
	WorkspaceInitializedWaitingOnAPIBindings = "WaitingOnAPIBindings"
	// WorkspaceInitializedWorkspaceTypeInvalid is a reason for the APIBindingsInitialized
	// condition that indicates something is invalid with the WorkspaceType (e.g. a cycle trying
	// to resolve all the transitive types).
	WorkspaceInitializedWorkspaceTypeInvalid = "WorkspaceTypesInvalid"
	// WorkspaceInitializedAPIBindingErrors is a reason for the APIBindingsInitialized condition that indicates there
	// were errors trying to initialize APIBindings for the workspace.
	WorkspaceInitializedAPIBindingErrors = "APIBindingErrors"
)
