/*
Copyright 2021 The KCP Authors.

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

	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// ClusterWorkspace defines a Kubernetes-cluster-like endpoint that holds a default set
// of resources and exhibits standard Kubernetes API semantics of CRUD operations. It represents
// the full life-cycle of the persisted data in this workspace in a KCP installation.
//
// ClusterWorkspace is a concrete type that implements a workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
type ClusterWorkspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceStatus `json:"status,omitempty"`
}

func (in *ClusterWorkspace) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ClusterWorkspace) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ClusterWorkspace{}
var _ conditions.Setter = &ClusterWorkspace{}

// ClusterWorkspaceSpec holds the desired state of the ClusterWorkspace.
type ClusterWorkspaceSpec struct {
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// type defines properties of the workspace both on creation (e.g. initial
	// resources and initially installed APIs) and during runtime (e.g. permissions).
	//
	// The type is a reference to a ClusterWorkspaceType in the same workspace
	// with the same name, but lower-cased. The ClusterWorkspaceType existence is
	// validated at admission during creation, with the exception of the
	// "Universal" type whose existence is not required but respected if it exists.
	// The type is immutable after creation. The use of a type is gated via
	// the RBAC clusterworkspacetypes/use resource permission.
	//
	// +optional
	// +kubebuilder:default:="Universal"
	Type string `json:"type,omitempty"`
}

// ClusterWorkspaceType specifies behaviour of workspaces of this type.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ClusterWorkspaceType struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceTypeSpec `json:"spec,omitempty"`
}

type ClusterWorkspaceTypeSpec struct {
	// initializers are set of a ClusterWorkspace on creation and must be
	// cleared by a controller before the workspace can be used. The workspace
	// will stay in the phase "Initializing" state until all initializers are cleared.
	//
	// +optional
	Initializers []ClusterWorkspaceInitializer `json:"initializers,omitempty"`

	// additionalWorkspaceLabels are a set of labels that will be added to a
	// ClusterWorkspace on creation.
	//
	// +optional
	AdditionalWorkspaceLabels map[string]string `json:"additionalWorkspaceLabels,omitempty"`
}

// ClusterWorkspaceTypeList is a list of cluster workspace types
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspaceType `json:"items"`
}

// ClusterWorkspaceInitializer is a unique string corresponding to a cluster workspace
// initialization controller for the given type of workspaces.
type ClusterWorkspaceInitializer string

// ClusterWorkspacePhaseType is the type of the current phase of the workspace
type ClusterWorkspacePhaseType string

const (
	ClusterWorkspacePhaseScheduling   ClusterWorkspacePhaseType = "Scheduling"
	ClusterWorkspacePhaseInitializing ClusterWorkspacePhaseType = "Initializing"
	ClusterWorkspacePhaseReady        ClusterWorkspacePhaseType = "Ready"
)

// ClusterWorkspaceStatus communicates the observed state of the ClusterWorkspace.
type ClusterWorkspaceStatus struct {
	// Phase of the workspace  (Scheduling / Initializing / Ready)
	Phase ClusterWorkspacePhaseType `json:"phase,omitempty"`

	// Current processing state of the ClusterWorkspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// Base URL where this ClusterWorkspace can be targeted.
	// This will generally be of the form: https://<workspace shard server>/cluster/<workspace name>.
	// But a workspace could also be targetable by a unique hostname in the future.
	//
	// +kubebuilder:validation:Pattern:https://[^/].*
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Contains workspace placement information.
	//
	// +optional
	Location ClusterWorkspaceLocation `json:"location,omitempty"`

	// initializers are set on creation by the system and must be cleared
	// by a controller before the workspace can be used. The workspace will
	// stay in the phase "Initializing" state until all initializers are cleared.
	//
	// A cluster workspace in "Initializing" state are gated via the RBAC
	// clusterworkspaces/initilize resource permission.
	//
	// +optional
	Initializers []ClusterWorkspaceInitializer `json:"initializers,omitempty"`
}

// These are valid conditions of workspace.
const (
	// WorkspaceScheduled represents status of the scheduling process for this workspace.
	WorkspaceScheduled conditionsv1alpha1.ConditionType = "WorkspaceScheduled"
	// WorkspaceReasonUnschedulable reason in WorkspaceScheduled WorkspaceCondition means that the scheduler
	// can't schedule the workspace right now, for example due to insufficient resources in the cluster.
	WorkspaceReasonUnschedulable = "Unschedulable"
	// WorkspaceReasonReasonUnknown reason in WorkspaceScheduled means that scheduler has failed for
	// some unexpected reason.
	WorkspaceReasonReasonUnknown = "Unknown"

	// WorkspaceShardValid represents status of the connection process for this workspace.
	WorkspaceShardValid conditionsv1alpha1.ConditionType = "WorkspaceShardValid"
	// WorkspaceShardValidReasonMissingCredentials reason in WorkspaceShardValid condition means that the
	// connection information in the referenced WorkspaceShard could not be found.
	WorkspaceShardValidReasonMissingCredentials = "MissingShardCredentials"
	// WorkspaceShardValidReasonURLInvalid reason in WorkspaceShardValid condition means that the
	// connection information in the referenced WorkspaceShard were invalid.
	WorkspaceShardValidReasonURLInvalid = "InvalidShardURL"
	// WorkspaceShardValidReasonShardNotFound reason in WorkspaceShardValid condition means that the
	// referenced WorkspaceShard object got deleted.
	WorkspaceShardValidReasonShardNotFound = "ShardNotFound"
	// WorkspaceShardValidReasonMissingConnectionInfo reason in WorkspaceShardValid condition means that the
	// referenced WorkspaceShard object lacks connection info.
	WorkspaceShardValidReasonMissingConnectionInfo = "MissingConnectionInfo"
)

// ClusterWorkspaceLocation specifies workspace placement information, including current, desired (target), and
// historical information.
type ClusterWorkspaceLocation struct {
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
	// +patchStrategy=merge
	// +patchMergeKey=name
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

// ClusterWorkspaceList is a list of ClusterWorkspace resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspace `json:"items"`
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

func (in *WorkspaceShard) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *WorkspaceShard) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &WorkspaceShard{}
var _ conditions.Setter = &WorkspaceShard{}

// WorkspaceShardSpec holds the desired state of the WorkspaceShard.
type WorkspaceShardSpec struct {
	// Credentials is a reference to the administrative credentials for this shard.
	Credentials corev1.SecretReference `json:"credentials"`
}

// WorkspaceShardStatus communicates the observed state of the WorkspaceShard.
type WorkspaceShardStatus struct {
	// Set of integer resources that workspaces can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the WorkspaceShard.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// Connection information for the WorkspaceShard.
	// +optional
	ConnectionInfo *ConnectionInfo `json:"connectionInfo,omitempty"`

	// Version of credentials last successfully loaded.
	// +optional
	CredentialsHash string `json:"credentialsHash,omitempty"`
}

// ConnectionInfo holds the information necessary to connect to a shard.
type ConnectionInfo struct {
	// Host must be a host string, a host:port pair, or a URL to the base of the apiserver.
	// If a URL is given then the (optional) Path of that URL represents a prefix that must
	// be appended to all request URIs used to access the apiserver. This allows a frontend
	// proxy to easily relocate all of the apiserver endpoints.
	// +kubebuilder:validation:Format=uri
	Host string `json:"host"`
	// APIPath is a sub-path that points to an API root.
	APIPath string `json:"apiPath"`
}

const (
	// WorkspaceShardCredentialsKey is the key in the referenced credentials secret where kubeconfig data lives.
	WorkspaceShardCredentialsKey = "kubeconfig"

	// WorkspaceShardCredentialsValid represents status of the credentialing process for this workspace shard.
	WorkspaceShardCredentialsValid conditionsv1alpha1.ConditionType = "WorkspaceShardCredentialsValid"
	// WorkspaceShardCredentialsReasonMissing reason in WorkspaceShardCredentialsValid condition means that the
	// credentials referenced in the WorkspaceShard could not be found.
	WorkspaceShardCredentialsReasonMissing = "Missing"
	// WorkspaceShardCredentialsReasonInvalid reason in WorkspaceShardCredentialsValid condition means that the
	// credentials referenced in the WorkspaceShard did not contain valid data in the correct key.
	WorkspaceShardCredentialsReasonInvalid = "Invalid"
)

// WorkspaceShardList is a list of workspace shards
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []WorkspaceShard `json:"items"`
}
