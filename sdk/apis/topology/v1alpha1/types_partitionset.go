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

	// shardSelector (optional) specifies filtering for shard targets.
	ShardSelector *metav1.LabelSelector `json:"shardSelector,omitempty"`
}

// PartitionSetStatus records the status of the PartitionSet.
type PartitionSetStatus struct {
	// count is the total number of partitions.
	Count uint16 `json:"count,omitempty"`

	// +optional

	// conditions is a list of conditions that apply to the APIExportEndpointSlice.
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *PartitionSet) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *PartitionSet) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of PartitionSet.
const (
	// PartitionSetValid reflects the validity of the PartitionSet spec.
	PartitionSetValid conditionsv1alpha1.ConditionType = "PartitionSetValid"
	// PartitionsReady indicates whether matching partitions could be created.
	PartitionsReady conditionsv1alpha1.ConditionType = "PartitionsReady"

	// PartitionSetInvalidSelectorReason indicates that the specified selector could not be
	// marshalled into a valid one.
	PartitionSetInvalidSelectorReason = "InvalidSelector"
	// ErrorGeneratingPartitionsReason indicates that the partitions could not be generated.
	ErrorGeneratingPartitionsReason = "ErrorGeneratingPartitions"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PartitionSetList is a list of PartitionSet resources.
type PartitionSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []PartitionSet `json:"items"`
}
