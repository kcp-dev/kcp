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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,path=apiexportendpointslices,singular=apiexportendpointslice
// +kubebuilder:printcolumn:name="Export",type="string",JSONPath=".spec.export.name"
// +kubebuilder:printcolumn:name="Partition",type="string",JSONPath=".spec.partition"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// APIExportEndpointSlice is a sink for the endpoints of an APIExport. These endpoints can be filtered by a Partition.
// They get consumed by the managers to start controllers and informers for the respective APIExport services.
type APIExportEndpointSlice struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec holds the desired state:
	// - the targeted APIExport
	// - an optional partition for filtering
	Spec APIExportEndpointSliceSpec `json:"spec,omitempty"`

	// status communicates the observed state:
	// the filtered list of endpoints for the APIExport service.
	// +optional
	Status APIExportEndpointSliceStatus `json:"status,omitempty"`
}

// APIExportEndpointSliceSpec defines the desired state of the APIExportEndpointSlice.
type APIExportEndpointSliceSpec struct {
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="APIExport reference must not be changed"

	// export points to the API export.
	APIExport ExportBindingReference `json:"export"`

	// +optional

	// partition (optional) points to a partition that is used for filtering the endpoints
	// of the APIExport part of the slice.
	Partition string `json:"partition,omitempty"`
}

// APIExportEndpointSliceStatus defines the observed state of APIExportEndpointSlice.
type APIExportEndpointSliceStatus struct {
	// +optional

	// conditions is a list of conditions that apply to the APIExportEndpointSlice.
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// +optional

	// endpoints contains all the URLs of the APIExport service.
	APIExportEndpoints []APIExportEndpoint `json:"endpoints,omitempty"`
}

// Using a struct provides an extension point

// APIExportEndpoint contains the endpoint information of an APIExport service for a specific shard.
type APIExportEndpoint struct {

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required

	// url is an APIExport virtual workspace URL.
	URL string `json:"url"`
}

func (in *APIExportEndpointSlice) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExportEndpointSlice) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of APIExportEndpointSlice in addition to
// APIExportValid and related reasons defined with the APIBinding type.
const (
	// PartitionValid is a condition for APIExportEndpointSlice that reflects the validity of the referenced Partition.
	PartitionValid conditionsv1alpha1.ConditionType = "PartitionValid"

	APIExportEndpointSliceURLsReady conditionsv1alpha1.ConditionType = "EndpointURLsReady"

	// PartitionInvalidReferenceReason is a reason for the PartitionValid condition of APIExportEndpointSlice that the
	// Partition reference is invalid.
	PartitionInvalidReferenceReason = "PartitionInvalidReference"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// APIExportEndpointSliceList is a list of APIExportEndpointSlice resources.
type APIExportEndpointSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExportEndpointSlice `json:"items"`
}
