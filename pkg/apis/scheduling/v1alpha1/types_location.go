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

// PlacementAnnotation is the type marshalled into the PlacementAnnotationKey annotation.
// TODO(sttts): doc and type this
type PlacementAnnotation map[string]PlacementState

// PlacementState is the state of a namespace placement state machine.
type PlacementState string

const (
	// PlacementStatePending means that the namespace is being adopted by the workload cluster.
	PlacementStatePending PlacementState = "Pending"
	// PlacementStateBound means that the namespace is adopted by the workload cluster.
	PlacementStateBound PlacementState = "Bound"
	// PlacementStateRemoving means that the namespace is being removed by the workload cluster.
	PlacementStateRemoving PlacementState = "Removing"
	// PlacementStateUnbound means that the namespace is removed and hence unbound from the workload cluster.
	PlacementStateUnbound PlacementState = "Unbound"
)

// Location represents a scheduling target for instances of a given type. A location
// is a projection and aggregation of instances referenced in a LocationDomain, used
// to express scheduling choices by a user. A location can subsume a number of instances.
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
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Available",type=string,JSONPath=`.status.availableInstances`,description="Available instances in this location"
// +kubebuilder:printcolumn:name="Instances",type=string,JSONPath=`.status.instances`,description="Instances in this location"
// +kubebuilder:printcolumn:name="Labels",type=string,JSONPath=`.metadata.annotation."scheduling.kcp.dev/labels"`,description="The common labels of this location"
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
	LocationSpecBase `json:",inline"`

	// domain is the LocationDomain this location belongs to.
	// +required
	// +kubebuilder:Required
	Domain LocationDomainReference `json:"domain"`

	// type defines which class of objects this location can schedule. A typical
	// type is "Workloads".
	//
	// +required
	// +kubebuilder:Required
	Type LocationDomainType `json:"type,omitempty"`
}

type LocationSpecBase struct {
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
}

// LocationDomainReference defines a reference to a location domain.
type LocationDomainReference struct {
	// workspace is the absolute workspace name (e.g. root:company) of the domain this location belongs to.
	//
	// +required
	// +kubebuilder:Required
	Workspace AbsoluteWorkspaceName `json:"workspace"`
	// name is the name of the location domain this location belongs to.
	//
	// +required
	// +kubebuilder:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AbsoluteWorkspaceName is an absolute workspace name, e.g. "root:company:workspace".
//
// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*[a-z0-9](:[a-z][a-z0-9-]*[a-z0-9])*$`
type AbsoluteWorkspaceName string

// AvailableSelectorLabel specifies a label with key name and possible values.
type AvailableSelectorLabel struct {
	// key is the name of the label.
	//
	// +required
	// +kubebuilder:Required
	Key LabelKey `json:"key"`

	// values are the possible values for this labels.
	//
	// +kubebuilder:validation:MinItems=1
	// +required
	// +kubebuilder:Required
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
