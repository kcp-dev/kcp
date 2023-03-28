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

// APIResourceImport describes an API resource imported from external clusters (either physical or logical)
// for a given GVR.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Location",type="string",JSONPath=`.spec.location`,priority=1
// +kubebuilder:printcolumn:name="Schema update strategy",type="string",JSONPath=`.spec.schemaUpdateStrategy`,priority=2
// +kubebuilder:printcolumn:name="API Version",type="string",JSONPath=`.metadata.annotations.apiresource\.kcp\.dev/apiVersion`,priority=3
// +kubebuilder:printcolumn:name="API Resource",type="string",JSONPath=`.spec.plural`,priority=4
// +kubebuilder:printcolumn:name="Compatible",type="string",JSONPath=`.status.conditions[?(@.type=="Compatible")].status`,priority=5
// +kubebuilder:printcolumn:name="Available",type="string",JSONPath=`.status.conditions[?(@.type=="Available")].status`,priority=6
type APIResourceImport struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec APIResourceImportSpec `json:"spec,omitempty"`

	// +optional
	Status APIResourceImportStatus `json:"status,omitempty"`
}

// SchemaUpdateStrategy defines the strategy for updating the
// correspondoing negotiated API resource
// based on the schema of this API Resource Import
type SchemaUpdateStrategyType string

const (
	// UpdateNever means that the corresponding negotiated API Resource will never be modified
	// to take in account the schema of the API resource import.
	// No LCD will be used, and the schema comparison will only
	// serve to known whether the schema of an API Resource import would be compatible with the
	// enforced CRD schema, and flag the API Resource import (and possibly the corresponding cluster location)
	// accordingly.
	UpdateNever SchemaUpdateStrategyType = "UpdateNever"

	// UpdateUnpublished means that the corresponding negotiated API Resource will be modified
	// to take in account the schema of the API resource import, but only for unpublished
	// negotiated API resources.
	// The modifications to the negotiated API resource will be based (if possible) on a LCD schema between
	// the schema of the resource import and the schema of the already-existing negotiated API resource.
	// Of course this is not valid if the negotiated API resource has been "enforced" by applying a CRD for
	// the same GVR manually
	UpdateUnpublished SchemaUpdateStrategyType = "UpdateUnpublished"

	// UpdateUnpublished means that the corresponding negotiated API Resource will be modified
	// to take in account the schema of the API resource import, even if the already-existing
	// negotiated API resource has already been published (as a CRD).
	// The modifications to the negotiated API resource will be based (if possible) on a LCD schema between
	// the schema of the resource import and the schema of the already-existing negotiated API resource.
	// Of course this is not valid if the negotiated API resource has been "enforced" by applying a CRD for
	// the same GVR manually
	UpdatePublished SchemaUpdateStrategyType = "UpdatePublished"
)

func (strategy SchemaUpdateStrategyType) CanUpdate(negotiatedAPIResourceIsPublished bool) bool {
	switch strategy {
	case UpdateNever:
		return false
	case UpdateUnpublished:
		return !negotiatedAPIResourceIsPublished
	case UpdatePublished:
		return true
	}
	return false
}

// APIResourceImportSpec holds the desired state of the APIResourceImport (from the client).
type APIResourceImportSpec struct {
	CommonAPIResourceSpec `json:",inline"`

	// SchemaUpdateStrategy defines the schema update strategy for this API Resource import.
	// Default value is UpdateUnpublished
	//
	// +optional
	SchemaUpdateStrategy SchemaUpdateStrategyType `json:"schemaUpdateStrategy,omitempty"`

	// Locaton the API resource is imported from
	// This field is required
	Location string `json:"location"`
}

// APIResourceImportConditionType is a valid value for APIResourceImportCondition.Type
type APIResourceImportConditionType string

const (
	// Compatible means that this API Resource import is compatible with the current
	// Negotiated API Resource
	Compatible APIResourceImportConditionType = "Compatible"
	// Available means that this API Resource import is compatible with the current
	// Negotiated API Resource, which has been published as a CRD
	Available APIResourceImportConditionType = "Available"
)

// APIResourceImportCondition contains details for the current condition of this negotiated api resource.
type APIResourceImportCondition struct {
	// Type is the type of the condition. Types include Compatible.
	Type APIResourceImportConditionType `json:"type"`
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

// APIResourceImportStatus communicates the observed state of the APIResourceImport (from the controller).
type APIResourceImportStatus struct {
	Conditions []APIResourceImportCondition `json:"conditions,omitempty"`
}

// APIResourceImportList is a list of APIResourceImport resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIResourceImportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIResourceImport `json:"items"`
}
