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
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Owner",type="string",JSONPath=".metadata.ownerReferences[*].name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Partition defines the selection of a set of shards along multiple dimensions.
// Partitions can get automatically generated through a partitioner or manually crafted.
type Partition struct {
	metav1.TypeMeta `json:",inline"`

	// +optional

	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	// +kubebuilder:validation:Required

	// spec holds the desired state.
	Spec PartitionSpec `json:"spec,omitempty"`
}

// PartitionSpec records the values defining the partition along multiple dimensions.
type PartitionSpec struct {
	// +optional

	// selector (optional) is a label selector that filters shard targets.
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PartitionList is a list of Partition resources.
type PartitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Partition `json:"items"`
}
