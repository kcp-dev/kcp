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

	// +kubebuilder:validation:MinLength=1
	Shard string `json:"shard"`

	// +optional
	TargetShard string `json:"targetShard,omitempty"`
}

// ShardStatus contains details for the current status of a workspace shard.
type ShardStatus struct {
	// Name of an active shard.
	//
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Resource version at which writes to this version should not be accepted.
	LiveBeforeResourceVersion string `json:"liveBeforeResourceVersion,omitempty"`

	// Resource version after which writes can be accepted on this shard.
	LiveAfterResourceVersion string `json:"liveAfterResourceVersion,omitempty"`
}

// WorkspacePhaseType is the type of the current phase of the workspace
type WorkspacePhaseType string

const (
	WorkspacePhaseInitializing WorkspacePhaseType = "Initializing"
	WorkspacePhaseAtive        WorkspacePhaseType = "Active"
	WorkspacePhaseTerminating  WorkspacePhaseType = "Terminating"
)

// WorkspaceStatus communicates the observed state of the Workspace.
type WorkspaceStatus struct {
	// Phase of the workspace  (Initializing / Active / Terminating)
	Phase WorkspacePhaseType `json:"phase,omitempty"`

	// List of shards related to this workspace.
	// The first shard in this list with LiveAfterResourceVersion is set is the active shard
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	Shards []ShardStatus `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
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
