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

package conditions

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// SetWorkspaceCondition sets the status condition. It either overwrites the existing one or creates a new one.
func SetWorkspaceCondition(workspace *v1alpha1.Workspace, newCondition v1alpha1.WorkspaceCondition) {
	newCondition.LastTransitionTime = v1.NewTime(time.Now())

	existingCondition := FindWorkspaceCondition(workspace, newCondition.Type)
	if existingCondition == nil {
		workspace.Status.Conditions = append(workspace.Status.Conditions, newCondition)
		return
	}

	if existingCondition.Status != newCondition.Status || existingCondition.LastTransitionTime.IsZero() {
		existingCondition.LastTransitionTime = newCondition.LastTransitionTime
	}

	existingCondition.Status = newCondition.Status
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

// RemoveWorkspaceCondition removes the status condition.
func RemoveWorkspaceCondition(workspace *v1alpha1.Workspace, conditionType v1alpha1.WorkspaceConditionType) {
	var newConditions []v1alpha1.WorkspaceCondition
	for _, condition := range workspace.Status.Conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}
	workspace.Status.Conditions = newConditions
}

// FindWorkspaceCondition returns the condition you're looking for or nil.
func FindWorkspaceCondition(workspace *v1alpha1.Workspace, conditionType v1alpha1.WorkspaceConditionType) *v1alpha1.WorkspaceCondition {
	for i := range workspace.Status.Conditions {
		if workspace.Status.Conditions[i].Type == conditionType {
			return &workspace.Status.Conditions[i]
		}
	}

	return nil
}

// IsWorkspaceConditionTrue indicates if the condition is present and strictly true.
func IsWorkspaceConditionTrue(workspace *v1alpha1.Workspace, conditionType v1alpha1.WorkspaceConditionType) bool {
	return IsWorkspaceConditionPresentAndEqual(workspace, conditionType, v1.ConditionTrue)
}

// IsWorkspaceConditionFalse indicates if the condition is present and false.
func IsWorkspaceConditionFalse(workspace *v1alpha1.Workspace, conditionType v1alpha1.WorkspaceConditionType) bool {
	return IsWorkspaceConditionPresentAndEqual(workspace, conditionType, v1.ConditionFalse)
}

// IsWorkspaceConditionPresentAndEqual indicates if the condition is present and equal to the given status.
func IsWorkspaceConditionPresentAndEqual(workspace *v1alpha1.Workspace, conditionType v1alpha1.WorkspaceConditionType, status v1.ConditionStatus) bool {
	for _, condition := range workspace.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

func IsWorkspaceUnschedulable(workspace *v1alpha1.Workspace) bool {
	if IsWorkspaceConditionFalse(workspace, v1alpha1.WorkspaceScheduled) {
		return FindWorkspaceCondition(workspace, v1alpha1.WorkspaceScheduled).Reason == v1alpha1.WorkspaceReasonUnschedulable
	}
	return false
}
