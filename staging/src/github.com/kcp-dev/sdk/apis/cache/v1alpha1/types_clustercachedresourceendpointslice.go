/*
Copyright 2025 The kcp Authors.

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

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,path=clustercachedresourceendpointslices,singular=clustercachedresourceendpointslice
// +kubebuilder:printcolumn:name="ClusterCachedResource",type="string",JSONPath=".spec.clusterCachedResource.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClusterCachedResourceEndpointSlice is a sink for the endpoints of ClusterCachedResource virtual workspaces.
type ClusterCachedResourceEndpointSlice struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec holds the desired state:
	// - the targeted ClusterCachedResource
	Spec ClusterCachedResourceEndpointSliceSpec `json:"spec,omitempty"`

	// status communicates the observed state:
	// the filtered list of endpoints for the Replication service.
	// +optional
	Status ClusterCachedResourceEndpointSliceStatus `json:"status,omitempty"`
}

// ClusterCachedResourceEndpointSliceSpec defines the desired state of the ClusterCachedResourceEndpointSlice.
type ClusterCachedResourceEndpointSliceSpec struct {
	// ClusterCachedResource points to the real ClusterCachedResource the slice is created for.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ClusterCachedResource reference must not be changed"
	ClusterCachedResource ClusterCachedResourceReference `json:"clusterCachedResource"`

	// export points to the APIExport that exports this ClusterCachedResourceEndpointSlice.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="APIExport reference must not be changed"
	APIExport ExportBindingReference `json:"export"`

	// partition points to a partition that is used for filtering the endpoints
	// of the ClusterCachedResource part of the slice.
	//
	// +optional
	Partition string `json:"partition,omitempty"`
}

// ExportBindingReference is a reference to an APIExport by cluster and name.
type ExportBindingReference struct {
	// path is a logical cluster path where the APIExport is defined.
	// If the path is unset, the logical cluster of the referencing object is used.
	//
	// +optional
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`

	// name is the name of the APIExport that describes the API.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	Name string `json:"name"`
}

// ClusterCachedResourceEndpointSliceStatus defines the observed state of ClusterCachedResourceEndpointSlice.
type ClusterCachedResourceEndpointSliceStatus struct {
	// conditions is a list of conditions that apply to the ClusterCachedResourceEndpointSlice.
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// endpoints contains all the URLs of the Replication service.
	//
	// +optional
	// +listType=map
	// +listMapKey=url
	ClusterCachedResourceEndpoints []ClusterCachedResourceEndpoint `json:"endpoints,omitempty"`

	// shardSelector is the selector used to filter the shards. It is used to filter the shards
	// when determining partition scope when deriving the endpoints. This is set by owning shard,
	// and is used by follower shards to determine if its inscope or not.
	//
	// +optional
	ShardSelector string `json:"shardSelector,omitempty"`
}

// Using a struct provides an extension point

// ClusterCachedResourceEndpoint contains the endpoint information of a Replication service for a specific shard.
type ClusterCachedResourceEndpoint struct {
	// url is Replication virtual workspace URL.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

func (in *ClusterCachedResourceEndpointSlice) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *ClusterCachedResourceEndpointSlice) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of ClusterCachedResourceEndpointSlice in addition to
// ClusterCachedResourceValid and related reasons defined with the APIBinding type.
const (
	// PartitionValid is a condition for ClusterCachedResourceEndpointSlice that reflects the validity of the referenced Partition.
	PartitionValid conditionsv1alpha1.ConditionType = "PartitionValid"

	// APIExportValid is a condition for ClusterCachedResourceEndpointSlice that reflects whether the referenced APIExport exists
	// and is accessible.
	APIExportValid conditionsv1alpha1.ConditionType = "APIExportValid"

	// EndpointURLsReady is a condition for ClusterCachedResourceEndpointSlice that reflects the readiness of the URLs.
	//
	// Deprecated: This condition is deprecated and will be removed in a future release.
	ClusterCachedResourceEndpointSliceURLsReady conditionsv1alpha1.ConditionType = "EndpointURLsReady"

	// PartitionInvalidReferenceReason is a reason for the PartitionValid condition of ClusterCachedResourceEndpointSlice that the
	// Partition reference is invalid.
	PartitionInvalidReferenceReason = "PartitionInvalidReference"

	// APIExportInvalidReferenceReason is a reason for the APIExportValid condition that the APIExport either does not
	// exist or does not reference this ClusterCachedResourceEndpointSlice.
	APIExportInvalidReferenceReason = "APIExportInvalidReference"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterCachedResourceEndpointSliceList is a list of ClusterCachedResourceEndpointSlice resources.
type ClusterCachedResourceEndpointSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterCachedResourceEndpointSlice `json:"items"`
}
