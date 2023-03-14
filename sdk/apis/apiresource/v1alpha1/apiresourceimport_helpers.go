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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SetCondition sets the status condition. It either overwrites the existing one or creates a new one.
func (apiResourceImport *APIResourceImport) SetCondition(newCondition APIResourceImportCondition) {
	newCondition.LastTransitionTime = metav1.NewTime(time.Now())

	existingCondition := apiResourceImport.FindCondition(newCondition.Type)
	if existingCondition == nil {
		apiResourceImport.Status.Conditions = append(apiResourceImport.Status.Conditions, newCondition)
		return
	}

	if existingCondition.Status != newCondition.Status || existingCondition.LastTransitionTime.IsZero() {
		existingCondition.LastTransitionTime = newCondition.LastTransitionTime
	}

	existingCondition.Status = newCondition.Status
	existingCondition.Reason = newCondition.Reason
	existingCondition.Message = newCondition.Message
}

// RemoveCondition removes the status condition.
func (apiResourceImport *APIResourceImport) RemoveCondition(conditionType APIResourceImportConditionType) {
	newConditions := []APIResourceImportCondition{}
	for _, condition := range apiResourceImport.Status.Conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}
	apiResourceImport.Status.Conditions = newConditions
}

// FindCondition returns the condition you're looking for or nil.
func (apiResourceImport *APIResourceImport) FindCondition(conditionType APIResourceImportConditionType) *APIResourceImportCondition {
	for i := range apiResourceImport.Status.Conditions {
		if apiResourceImport.Status.Conditions[i].Type == conditionType {
			return &apiResourceImport.Status.Conditions[i]
		}
	}

	return nil
}

// IsConditionTrue indicates if the condition is present and strictly true.
func (apiResourceImport *APIResourceImport) IsConditionTrue(conditionType APIResourceImportConditionType) bool {
	return apiResourceImport.IsConditionPresentAndEqual(conditionType, metav1.ConditionTrue)
}

// IsConditionFalse indicates if the condition is present and false.
func (apiResourceImport *APIResourceImport) IsConditionFalse(conditionType APIResourceImportConditionType) bool {
	return apiResourceImport.IsConditionPresentAndEqual(conditionType, metav1.ConditionFalse)
}

// IsConditionPresentAndEqual indicates if the condition is present and equal to the given status.
func (apiResourceImport *APIResourceImport) IsConditionPresentAndEqual(conditionType APIResourceImportConditionType, status metav1.ConditionStatus) bool {
	for _, condition := range apiResourceImport.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsAPIResourceImportConditionEquivalent returns true if the lhs and rhs are equivalent except for times.
func IsAPIResourceImportConditionEquivalent(lhs, rhs *APIResourceImportCondition) bool {
	if lhs == nil && rhs == nil {
		return true
	}
	if lhs == nil || rhs == nil {
		return false
	}

	return lhs.Message == rhs.Message && lhs.Reason == rhs.Reason && lhs.Status == rhs.Status && lhs.Type == rhs.Type
}

// GVR returns the GVR that this APIResourceImport represents.
func (apiResourceImport *APIResourceImport) GVR() metav1.GroupVersionResource {
	return metav1.GroupVersionResource{
		Group:    apiResourceImport.Spec.GroupVersion.Group,
		Version:  apiResourceImport.Spec.GroupVersion.Version,
		Resource: apiResourceImport.Spec.Plural,
	}
}
