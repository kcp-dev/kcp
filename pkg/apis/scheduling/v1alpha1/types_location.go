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

const (
	// LocationLabelsStringAnnotationKey is the label key for the label holding a string
	// representation of the location labels in order to use them in a table column in the CLI.
	LocationLabelsStringAnnotationKey = "scheduling.kcp.dev/labels"

	// PlacementAnnotationKey is the label key for the label holding a PlacementAnnotation struct.
	PlacementAnnotationKey = "scheduling.kcp.dev/placement"
)

// Location represents a set of instances of a scheduling resource type acting a target
// of scheduling.
//
// The location is chosen by the user (in the future) through a Placement object, while
// the instance is chosen by the scheduler depending on considerations like load
// or available resources, or further node selectors specified by the user.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=`.spec.resource.resource`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.availableInstances`,description="Available instances in this location"
// +kubebuilder:printcolumn:name="Instances",type=string,JSONPath=`.status.instances`,description="Instances in this location"
// +kubebuilder:printcolumn:name="Labels",type=string,JSONPath=`.metadata.annotations['scheduling\.kcp\.dev/labels']`,description="The common labels of this location"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Location struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec LocationSpec `json:"spec,omitempty"`

	// +optional
	Status LocationStatus `json:"status,omitempty"`
}

// LocationSpec holds the desired state of the Location.
type LocationSpec struct {
	// resource is the group-version-resource of the instances that are subject to this location.
	//
	// +required
	// +kubebuilder:validation:Required
	Resource GroupVersionResource `json:"resource"`

	// description is a human-readable description of the location.
	//
	// +optional
	Description string `json:"description,omitempty"`

	// availableSelectorLabels is a list of labels that can be used to select an
	// instance at this location in a placement object.
	//
	// +listType=map
	// +listMapKey=key
	AvailableSelectorLabels []AvailableSelectorLabel `json:"availableSelectorLabels,omitempty"`

	// instanceSelector chooses the instances that will be part of this location.
	//
	// Note that these labels are not what is shown in the Location objects to
	// the user. Depending on context, both will match or won't match.
	//
	// +optional
	InstanceSelector *metav1.LabelSelector `json:"instanceSelector,omitempty"`
}

// GroupVersionResource unambiguously identifies a resource.
type GroupVersionResource struct {
	// group is the name of an API group.
	//
	// +kubebuilder:validation:Pattern=`^(|[a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?)$`
	// +kubebuilder:validation:Enum="workload.kcp.dev"
	// +optional
	Group string `json:"group,omitempty"`

	// version is the version of the API.
	//
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9]$`
	// +kubebuilder:validation:MinLength:1
	// +kubebuilder:validation:Enum="v1alpha1"
	// +required
	// +kubebuilder:validation:Required
	Version string `json:"version"`

	// resource is the name of the resource.
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9]$`
	// +kubebuilder:validation:MinLength:1
	// +kubebuilder:validation:Enum="synctargets"
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
}

// AvailableSelectorLabel specifies a label with key name and possible values.
type AvailableSelectorLabel struct {
	// key is the name of the label.
	//
	// +required
	// +kubebuilder:validation:Required
	Key LabelKey `json:"key"`

	// values are the possible values for this labels.
	//
	// +kubebuilder:validation:MinItems=1
	// +required
	// +kubebuilder:validation:Required
	// +listType=set
	Values []LabelValue `json:"values"`

	// description is a human readable description of the label.
	//
	// +optional
	Description string `json:"description,omitempty"`
}

// LabelKey is a key for a label.
//
// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?/)?([a-zA-Z0-9][-a-zA-Z0-9_.]{0,61})?[a-zA-Z0-9]$`
// +kubebuilder:validation:MaxLength=255
type LabelKey string

// LabelValue specifies a value of a label.
//
// +kubebuilder:validation:Pattern=`^(|([a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?/)?([a-zA-Z0-9][-a-zA-Z0-9_.]{0,61})?[a-zA-Z0-9])$`
// +kubebuilder:validation:MaxLength=63
type LabelValue string

// LocationStatus defines the observed state of Location.
type LocationStatus struct {
	// instances is the number of actual instances at this location.
	Instances *uint32 `json:"instances,omitempty"`

	// available is the number of actual instances that are available at this location.
	AvailableInstances *uint32 `json:"availableInstances,omitempty"`
}

// LocationList is a list of locations.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Location `json:"items"`
}
