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

	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

// Placement specifies how a namespace is scheduled on instances of locations.
//
// TODO(sttts): maybe we also need non-namespaced ClusterPlacement?
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespace,categories=kcp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
type Placement struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec PlacementSpec `json:"spec,omitempty"`
	// +optional
	Status *PlacementStatus `json:"status,omitempty"`
}

// PlacementSpec is the specification of a placement.
type PlacementSpec struct {
	// locations selects a number of different locations. The scheduler will schedule the
	// namespace to all of these, potentially to multiple instances of each location
	// (depending on strategy).
	//
	// scheduling.kcp.dev/placement: { # <location_name> -> “Pending|Bound|Removing|Unbound”, equivalent of spec.nodeName
	//  “location-a+instance-id”: “Removing”,
	//  “location-b+instance-id”: “Bound",
	//  “location-c+instance-id”: “Pending”
	// }
	//
	// Note: we could add another state "Active", meaning the syncer has done its work. A little bit like "Running". But that
	//       might get confusing because e.g. a pod is not really running just because the object exists.
	//
	// The namespace scheduler for workloads will watch objects for scheduling.kcp.dev/placement and do:
	// - adds the `cluster.workloads.kcp.dev/<workload-cluster-id>: ""` when state is Pending, and sets placement state `Bound`.
	// - sets the state to `Unbound` if the state is `Removing` and there is no `cluster.workloads.kcp.dev/<workload-cluster-id>`.
	//
	//   The placement flow for workloads is this:
	//
	//   1. the `cluster.workloads.kcp.dev/<workload-cluster-id>: ""` label is set.
	//   2. some actor (depending on (spreading) strategy) will notice the empty string, and replace it with
	//      "Sync" after adding a `workloads.kcp.dev/strategy-details-<workload-cluster-id>` annotation that will
	//      contains concrete syncing strategy details (not modelled here, some API from syncer and virtual workspace
	//      to be defined; possibly different per object kind).
	//
	//         labels:
	//   	     cluster.workloads.kcp.dev/<workload-cluster-id>: "Sync"
	//         annotations:
	//   	     transformation.workloads.kcp.dev/<workload-cluster-id>: "<some workload-service specific placement details, e.g. instances>"
	//
	//      Examples of those strategy detail annotations: TODO(davidfestal): to be modelled in detail
	//      - ingress will need draining parameters: {"apiVersion":"syncer.kcp.dev/v1", "kind":"IngressSplitter", "state": "drain", "drain_timeout": "30s", "drain_grace_period": "5s"}
	//      - deployment will need replica counts: {"apiVersion":"syncer.kcp.dev/v1", "kind":"DeploymentSplitter" "replicas": 3}
	//      - potentially even: {"apiVersion":"syncer.kcp.dev/v1", "kind": "CEL", "transformation": "<CEL>"}
	//
	//   3. the syncer and the syncer virtual workspace apiserver play together to follow those syncing strategy details as soon as the `cluster.workloads.kcp.dev/<workload-cluster-id>` has the `Sync` value
	//
	//   The deletion flow for workloads is this:
	//   1. some external actor decides that the object should be removed from a location instance, and for that sets the
	//      state to `Removing` in the `location.kcp.dev/placement` annotation.
	//   2. the namespace scheduler will notice the `Removing` state and will set the `cluster.workloads.kcp.dev/<workload-cluster-id>` label value to `Delete`
	//   3. the syncer virtual workspace apiserver will notice the `Delete` value of the `cluster.workloads.kcp.dev/<workload-cluster-id>` label.
	//      It will check that the annotation is not present or empty:
	//
	//        finalizers.workloads.kcp.dev/<workload-cluster-id> = "controller1,controller2"
	//
	//      and in that case, will virtually mark the object as deleting (by setting a DeletionTimestamp) for that location instance.
	//   4. the syncer will notice the deletion timestamp, delete the object downstream (this means that the deletion timestamp
	//      is set downstream), then waits for deletion to complete (all finalizers are removed downstream).
	//      Then the syncer will remove its (virtual) finalizer upstream in the syncer virtual workspace apiserver view.
	//   4. the syncer virtual workspace apiserver is waiting for the syncer finalizer being removed, and then will
	//      virtually delete the object for the syncer by removing the `cluster.workloads.kcp.dev/<workload-cluster-id>`
	//      label.
	//   5. the syncer will notice a delete event from upstream, but there is nothing to do for the syncer.
	//   6. the namespace scheduler will notice the removed `cluster.workloads.kcp.dev/<workload-cluster-id>` and will set
	//	    placement state to Unbound.
	//
	Locations []PlacementLocationSelector `json:"locations,omitempty"`

	// type is the location instance type, e.g. "Workloads".
	//
	// +required
	// +kubebuilder:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
}

type PlacementStatus struct {
	// locations are the selected locations for the namespace according to the spec.
	// +listType=map
	// +listMapKey=name
	Locations []PlacementLocationStatus `json:"locations,omitempty"`

	// conditions is a list of conditions that apply to the Placement as a whole.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *Placement) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *Placement) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

type PlacementLocationStatus struct {
	// name of the location.
	Name string `json:"name"`

	// instance is the identifier of the location instance.
	Instance string `json:"instance"`

	// phase is the current state of the location for the namespace.
	//
	// +optional
	// +kubebuilder:validation:Enum=Pending;Bound;Removing;Unbound
	Phase string `json:"phase,omitempty"`

	// conditions is a list of conditions that apply to one location of the Placement.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *PlacementLocationStatus) GetConditions() conditionsv1alpha1.Conditions {
	return in.Conditions
}

func (in *PlacementLocationStatus) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Conditions = conditions
}

// PlacementLocationStrategy is the strategy for a placement.
type PlacementLocationStrategy struct {
	// +optional
	// +kubebuilder:validation:Enum={"Spread", "HihghlyAvailable"}
	Type string `json:"type,omitempty"`
	// +optional
	Spread *PlacementLocationStrategySpread `json:"spread,omitempty"`
	// +optional
	HighlyAvailable *PlacementLocationStrategyHighlyAvailable `json:"highlyAvailable,omitempty"`
}

type PlacementLocationStrategySpread struct {
	// The number of instances to schedule.
	//
	// +optional
	Count int32 `json:"count,omitempty"`
}

type PlacementLocationStrategyHighlyAvailable struct {
	// +required
	// +kubebuilder:Required
	// +kubebuilder:validation:Enum={"FailOver", "ColdStandby"}
	Strategy string `json:"strategy,omitempty"`
}

// PlacementLocationSelector is a selector for a location. This selects different locations
// according to the given labels.
//
// In the future, concepts like affinity and anti-affinity might be supported.
type PlacementLocationSelector struct {
	// labels select the location.
	Labels metav1.LabelSelector `json:"labels,omitempty"`

	// strategy defines which instances of selected location will be scheduled to.
	//
	// +optional
	Strategy *PlacementLocationStrategy `json:"strategy,omitempty"`
}

// PlacementList is a list of placements.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Location `json:"items"`
}
