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

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// placement defines a selection rule to choose ONE location for MULTIPLE namespaces in a workspace.
//
// placement is in Pending state initially. When a location is selected by the placement, the placement
// turns to Unbound state. In Pending or Unbound state, the selection rule can be updated to select another location.
// When the a namespace is annotated by another controller or user with the key of "scheudling.kcp.dev/placement",
// the namespace will pick one placement, and this placement is transfered to Bound state. Any update to spec of the placement
// is ignored in Bound state and reflected in the conditions. The placement will turns back to Unbound state when no namespace
// uses this placement any more.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type Placement struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PlacementSpec `json:"spec,omitempty"`

	// +optional
	Status PlacementStatus `json:"status,omitempty"`
}

type PlacementSpec struct {
	// loacationSelectors represents a slice of label selector to select a location, these label selectors
	// are logically ORed.
	LoacationSelectors []metav1.LabelSelector `json:"locationSelectors,omitempty"`

	// locationResource is the group-version-resource of the instances that are subject to the locations to select.
	//
	// +required
	// +kubebuilder:Required
	LocationResource GroupVersionResource `json:"locationResource"`

	// namespaceSelector is a label selector to select ns. It match all ns by default, but can be specified to
	// a certain set of ns.
	// +optional
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`

	// locationWorkspace is an absolute reference to a workspace for the location. If it is not set, the workspace of
	// APIBinding will be used.
	// +optional
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	LocationWorkspace string `json:"locationWorkspace,omitempty"`
}

type PlacementStatus struct {
	// phase is the current phase of the placement
	//
	// +kubebuilder:default=Pending
	// +kubebuilder:validation:Enum=Pending;Bound;Unbound
	Phase PlacementPhase `json:"phase,omitempty"`

	// selectedLocation is the location that a picked by this placement.
	// +optional
	SelectedLocation *LocationReference `json:"selectedLocation,omitempty"`

	// Current processing state of the Placement.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// LocationReference describes a loaction that are provided in the specified Workspace.
type LocationReference struct {
	// path is an absolute reference to a workspace, e.g. root:org:ws. The workspace must
	// be some ancestor or a child of some ancestor.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`

	// Name of the Location.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	LocationName string `json:"exportName"`
}

type PlacementPhase string

const (
	// PlacementPending is the phase that the location has not been selected for this placement.
	PlacementPending = "Pending"

	// PlacementUnbound is the phase that the location has been selected by the placement, but
	// no namespace is bound to this placement yet.
	PlacementUnbound = "Unbound"

	// PlacementBound is the phase that the location has been selected by the placement, and at
	// least one namespace has been bound to this placement.
	PlacementBound = "Bound"
)

// PlacementList is a list of locations.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Placement `json:"items"`
}
