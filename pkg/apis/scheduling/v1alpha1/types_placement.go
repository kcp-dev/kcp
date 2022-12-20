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
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// Placement defines a selection rule to choose ONE location for MULTIPLE namespaces in a workspace.
//
// placement is in Pending state initially. When a location is selected by the placement, the placement
// turns to Unbound state. In Pending or Unbound state, the selection rule can be updated to select another location.
// When the a namespace is annotated by another controller or user with the key of "scheduling.kcp.dev/placement",
// the namespace will pick one placement, and this placement is transferred to Bound state. Any update to spec of the placement
// is ignored in Bound state and reflected in the conditions. The placement will turn back to Unbound state when no namespace
// uses this placement any more.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Placement struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PlacementSpec `json:"spec,omitempty"`

	// +optional
	Status PlacementStatus `json:"status,omitempty"`
}

func (in *Placement) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *Placement) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &Placement{}
var _ conditions.Setter = &Placement{}

type PlacementSpec struct {
	// locationSelectors represents a slice of label selector to select a location, these label selectors
	// are logically ORed.
	LocationSelectors []metav1.LabelSelector `json:"locationSelectors,omitempty"`

	// locationResource is the group-version-resource of the instances that are subject to the locations to select.
	//
	// +required
	// +kubebuilder:validation:Required
	LocationResource GroupVersionResource `json:"locationResource"`

	// namespaceSelector is a label selector to select ns. It match all ns by default, but can be specified to
	// a certain set of ns.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// locationWorkspace is an absolute reference to a workspace for the location. If it is not set, the workspace of
	// APIBinding will be used.
	// +optional
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
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

// LocationReference describes a location that are provided in the specified Workspace.
type LocationReference struct {
	// path is an absolute reference to a workspace, e.g. root:org:ws. The workspace must
	// be some ancestor or a child of some ancestor.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`

	// Name of the Location.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	LocationName string `json:"locationName"`
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

const (
	// PlacementReady is a condition type for placement representing that the placement is ready
	// for scheduling. The placement is NOT ready when location cannot be found for the placement,
	// or the selected location does not match the placement spec.
	PlacementReady conditionsv1alpha1.ConditionType = "Ready"

	// LocationNotFoundReason is a reason for PlacementReady condition that a location cannot be
	// found for this placement.
	LocationNotFoundReason = "LocationNotFound"

	// LocationInvalidReason is a reason for PlacementReady condition that a location is not valid
	// for this placement anymore.
	LocationInvalidReason = "LocationInvalid"

	// LocationNotMatchReason is a reason for PlacementReady condition that no matched location for
	// this placement can be found.
	LocationNotMatchReason = "LocationNoMatch"

	// PlacementScheduled is a condition type for placement representing that a scheduling decision is
	// made. The placement is NOT Scheduled when no valid schedule decision is available or an error
	// occurs.
	PlacementScheduled conditionsv1alpha1.ConditionType = "Scheduled"

	// ScheduleLocationNotFound is a reason for PlacementScheduled condition that location is not available for scheduling.
	ScheduleLocationNotFound = "ScheduleLocationNotFound"

	// ScheduleNoValidTargetReason is a reason for PlacementScheduled condition that no valid target is scheduled
	// for this placement.
	ScheduleNoValidTargetReason = "NoValidTarget"
)

// PlacementList is a list of locations.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Placement `json:"items"`
}
