/*
Copyright 2021 The Authors

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workspace describes how clients access (kubelike) APIs
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type Workspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec WorkspaceSpec `json:"spec,omitempty"`

	// +optional
	Status WorkspaceStatus `json:"status,omitempty"`
}

// WorkspaceSpec holds the desired state of the Workspace.
type WorkspaceSpec struct {
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// WorkspacePhaseType is the type of the current phase of the workspace
type WorkspacePhaseType string

const (
	WorkspacePhaseInitializing WorkspacePhaseType = "Initializing"
	WorkspacePhaseActive       WorkspacePhaseType = "Active"
	WorkspacePhaseTerminating  WorkspacePhaseType = "Terminating"
)

// WorkspaceStatus communicates the observed state of the Workspace.
type WorkspaceStatus struct {
	// Phase of the workspace  (Initializing / Active / Terminating)
	Phase WorkspacePhaseType `json:"phase,omitempty"`
	// +optional
	Conditions []WorkspaceCondition `json:"conditions,omitempty"`

	// Base URL where this Workspace can be targeted.
	// This will generally be of the form: https://<workspace shard server>/cluster/<workspace name>.
	// But a workspace could also be targetable by a unique hostname in the future.
	//
	// +kubebuilder:validation:Pattern:https://[^/].*
	BaseURL string `json:"baseURL"`

	// Contains workspace placement information.
	//
	// +optional
	Location WorkspaceLocation `json:"location,omitempty"`
}

// WorkspaceConditionType defines the condition of the workspace
type WorkspaceConditionType string

// These are valid conditions of workspace.
const (
	// WorkspaceScheduled represents status of the scheduling process for this workspace.
	WorkspaceScheduled WorkspaceConditionType = "WorkspaceScheduled"
	// WorkspaceReasonUnschedulable reason in WorkspaceScheduled WorkspaceCondition means that the scheduler
	// can't schedule the workspace right now, for example due to insufficient resources in the cluster.
	WorkspaceReasonUnschedulable = "Unschedulable"
)

// WorkspaceCondition represents workspace's condition
type WorkspaceCondition struct {
	Type   WorkspaceConditionType `json:"type,omitempty"`
	Status metav1.ConditionStatus `json:"status,omitempty"`
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitempty"`
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// WorkspaceLocation specifies workspace placement information, including current, desired (target), and
// historical information.
type WorkspaceLocation struct {
	// Current workspace placement (shard).
	//
	// +optional
	Current string `json:"current,omitempty"`

	// Target workspace placement (shard).
	//
	// +optional
	Target string `json:"target,omitempty"`

	// Historical placement details (including current and target).
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	History []ShardStatus `json:"history,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// ShardStatus contains details for the current status of a workspace shard.
type ShardStatus struct {
	// Name of an active WorkspaceShard.
	//
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Resource version at which writes to this shard should not be accepted.
	LiveBeforeResourceVersion string `json:"liveBeforeResourceVersion,omitempty"`

	// Resource version after which writes can be accepted on this shard.
	LiveAfterResourceVersion string `json:"liveAfterResourceVersion,omitempty"`
}

// WorkspaceList is a list of Workspace resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Workspace `json:"items"`
}

// WorkspaceShard describes a Shard (== KCP instance) on which a number of
// workspaces will live
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type WorkspaceShard struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec WorkspaceShardSpec `json:"spec,omitempty"`

	// +optional
	Status WorkspaceShardStatus `json:"status,omitempty"`
}

// WorkspaceShardSpec holds the desired state of the WorkspaceShard.
type WorkspaceShardSpec struct {
}

// WorkspaceShardStatus communicates the observed state of the WorkspaceShard.
type WorkspaceShardStatus struct {
	// Set of integer resources that workspaces can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`
}

// WorkspaceShardList is a list of Workspace shards
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []WorkspaceShard `json:"items"`
}
