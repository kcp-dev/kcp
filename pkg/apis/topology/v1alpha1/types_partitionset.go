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
)

// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Count",type="string",JSONPath=".status.count",description="Count of the partitions belonging the PartitionSet"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// PartitionSet defines a target domain and dimensions to divide a set of shards into 1 or more partitions.
type PartitionSet struct {
	metav1.TypeMeta `json:",inline"`

	// +optional

	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required

	// spec holds the desired state.
	Spec PartitionSetSpec `json:"spec,omitempty"`

	// +optional

	// status holds information about the current status
	Status PartitionSetStatus `json:"status,omitempty"`
}

// PartitionSetSpec records dimensions and a target domain for the partitioning.
type PartitionSetSpec struct {
	// +optional

	// dimensions (optional) are used to group shards into partitions
	Dimensions []string `json:"dimensions,omitempty"`

	// +optional

	// selector (optional) is a label selector that filters shard targets.
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// PartitionSetStaus records the status of the PartitionSet.
type PartitionSetStatus struct {
	// count is the total number of partitions.
	Count uint16 `json:"count,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PartitionSetList is a list of PartitionSet resources
type PartitionSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []PartitionSet `json:"items"`
}
