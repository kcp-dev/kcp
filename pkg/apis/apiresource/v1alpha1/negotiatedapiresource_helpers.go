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
func (negotiatedApiResource *NegotiatedAPIResource) SetCondition(newCondition NegotiatedAPIResourceCondition) {
	newCondition.LastTransitionTime = metav1.NewTime(time.Now())

	existingCondition := negotiatedApiResource.FindCondition(newCondition.Type)
	if existingCondition == nil {
		negotiatedApiResource.Status.Conditions = append(negotiatedApiResource.Status.Conditions, newCondition)
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
func (negotiatedApiResource *NegotiatedAPIResource) RemoveCondition(conditionType NegotiatedAPIResourceConditionType) {
	newConditions := []NegotiatedAPIResourceCondition{}
	for _, condition := range negotiatedApiResource.Status.Conditions {
		if condition.Type != conditionType {
			newConditions = append(newConditions, condition)
		}
	}
	negotiatedApiResource.Status.Conditions = newConditions
}

// FindCondition returns the condition you're looking for or nil.
func (negotiatedApiResource *NegotiatedAPIResource) FindCondition(conditionType NegotiatedAPIResourceConditionType) *NegotiatedAPIResourceCondition {
	for i := range negotiatedApiResource.Status.Conditions {
		if negotiatedApiResource.Status.Conditions[i].Type == conditionType {
			return &negotiatedApiResource.Status.Conditions[i]
		}
	}

	return nil
}

// IsConditionTrue indicates if the condition is present and strictly true.
func (negotiatedApiResource *NegotiatedAPIResource) IsConditionTrue(conditionType NegotiatedAPIResourceConditionType) bool {
	return negotiatedApiResource.IsConditionPresentAndEqual(conditionType, metav1.ConditionTrue)
}

// IsConditionFalse indicates if the condition is present and false.
func (negotiatedApiResource *NegotiatedAPIResource) IsConditionFalse(conditionType NegotiatedAPIResourceConditionType) bool {
	return negotiatedApiResource.IsConditionPresentAndEqual(conditionType, metav1.ConditionFalse)
}

// IsConditionPresentAndEqual indicates if the condition is present and equal to the given status.
func (negotiatedApiResource *NegotiatedAPIResource) IsConditionPresentAndEqual(conditionType NegotiatedAPIResourceConditionType, status metav1.ConditionStatus) bool {
	for _, condition := range negotiatedApiResource.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == status
		}
	}
	return false
}

// IsNegotiatedAPIResourceConditionEquivalent returns true if the lhs and rhs are equivalent except for times.
func IsNegotiatedAPIResourceConditionEquivalent(lhs, rhs *NegotiatedAPIResourceCondition) bool {
	if lhs == nil && rhs == nil {
		return true
	}
	if lhs == nil || rhs == nil {
		return false
	}

	return lhs.Message == rhs.Message && lhs.Reason == rhs.Reason && lhs.Status == rhs.Status && lhs.Type == rhs.Type
}

// GVR returns the GVR that this NegotiatedAPIResource represents.
func (negotiatedApiResource *NegotiatedAPIResource) GVR() metav1.GroupVersionResource {
	return metav1.GroupVersionResource{
		Group:    negotiatedApiResource.Spec.GroupVersion.Group,
		Version:  negotiatedApiResource.Spec.GroupVersion.Version,
		Resource: negotiatedApiResource.Spec.Plural,
	}
}
