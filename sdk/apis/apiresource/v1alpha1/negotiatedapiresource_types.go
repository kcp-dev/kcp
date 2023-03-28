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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NegotiatedAPIResource describes the result of either the normalization of
// any number of imports of an API resource from external clusters (either physical or logical),
// or the the manual application of a CRD version for the corresponding GVR.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Publish",type="boolean",JSONPath=`.spec.publish`,priority=1
// +kubebuilder:printcolumn:name="API Version",type="string",JSONPath=`.metadata.annotations.apiresource\.kcp\.dev/apiVersion`,priority=3
// +kubebuilder:printcolumn:name="API Resource",type="string",JSONPath=`.spec.plural`,priority=4
// +kubebuilder:printcolumn:name="Published",type="string",JSONPath=`.status.conditions[?(@.type=="Published")].status`,priority=5
// +kubebuilder:printcolumn:name="Enforced",type="string",JSONPath=`.status.conditions[?(@.type=="Enforced")].status`,priority=6
type NegotiatedAPIResource struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec NegotiatedAPIResourceSpec `json:"spec,omitempty"`

	// +optional
	Status NegotiatedAPIResourceStatus `json:"status,omitempty"`
}

// NegotiatedAPIResourceSpec holds the desired state of the NegotiatedAPIResource (from the client).
type NegotiatedAPIResourceSpec struct {
	CommonAPIResourceSpec `json:",inline"`
	Publish               bool `json:"publish,omitempty"`
}

// NegotiatedAPIResourceConditionType is a valid value for NegotiatedAPIResourceCondition.Type
type NegotiatedAPIResourceConditionType string

const (
	// Submitted means that this negotiated API Resource has been submitted
	// to the logical cluster as an applied CRD
	Submitted NegotiatedAPIResourceConditionType = "Submitted"

	// Published means that this negotiated API Resource has been published
	// to the logical cluster as an installed and accepted CRD
	// If the API Resource has been submitted
	// to the logical cluster as an applied CRD, but the CRD could not be published
	// correctly due to various reasons (non-structural schema, non-accepted names, ...)
	// then the Published condition will be false
	Published NegotiatedAPIResourceConditionType = "Published"

	// Enforced means that a CRD version for the same GVR has been manually applied,
	// so that the current schema of the negotiated api resource has been forcbly
	// replaced by the schema of the manually-applied CRD.
	// In such a condition, changes in `APIResourceImport`s would not, by any mean,
	// impact the negotiated schema: no LCD will be used, and the schema comparison will only
	// serve to known whether the schema of an API Resource import would be compatible with the
	// enforced CRD schema, and flag the API Resource import (and possibly the corresponding cluster location)
	// accordingly.
	Enforced NegotiatedAPIResourceConditionType = "Enforced"
)

// NegotiatedAPIResourceCondition contains details for the current condition of this negotiated api resource.
type NegotiatedAPIResourceCondition struct {
	// Type is the type of the condition. Types include Submitted, Published, Refused and Enforced.
	Type NegotiatedAPIResourceConditionType `json:"type"`
	// Status is the status of the condition.
	// Can be True, False, Unknown.
	Status metav1.ConditionStatus `json:"status"`
	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// Unique, one-word, CamelCase reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Human-readable message indicating details about last transition.
	// +optional
	Message string `json:"message,omitempty"`
}

// NegotiatedAPIResourceStatus communicates the observed state of the NegotiatedAPIResource (from the controller).
type NegotiatedAPIResourceStatus struct {
	Conditions []NegotiatedAPIResourceCondition `json:"conditions,omitempty"`
}

// NegotiatedAPIResourceList is a list of NegotiatedAPIResource resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type NegotiatedAPIResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NegotiatedAPIResource `json:"items"`
}
