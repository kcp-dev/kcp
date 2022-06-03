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
	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// RootCluster is the root of ClusterWorkspace based logical clusters.
var RootCluster = logicalcluster.New("root")

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
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.baseURL`,description="URL to access the workspace"
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
	// +kubebuilder:validation:Pattern=`^[A-Z][a-zA-Z0-9]+$`
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
type ClusterWorkspaceInitializer struct {
	// name is a unique string corresponding to a cluster workspace
	// initialization controller for the given type of workspaces.
	//
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`
	Name string `json:"name"`

	// path is an absolute reference to the workspace that owns this initializer, e.g. root:org:ws.
	//
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`
}

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
	// clusterworkspaces/initialize resource permission.
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

	// WorkspaceShardValid represents status of the connection process for this cluster workspace.
	WorkspaceShardValid conditionsv1alpha1.ConditionType = "WorkspaceShardValid"
	// WorkspaceShardValidReasonShardNotFound reason in WorkspaceShardValid condition means that the
	// referenced ClusterWorkspaceShard object got deleted.
	WorkspaceShardValidReasonShardNotFound = "ShardNotFound"

	// WorkspaceDeletionContentSuccess represents the status that all resources in the workspace is deleting
	WorkspaceDeletionContentSuccess conditionsv1alpha1.ConditionType = "WorkspaceDeletionContentSuccess"

	// WorkspaceContentDeleted represents the status that all resources in the workspace is deleted.
	WorkspaceContentDeleted conditionsv1alpha1.ConditionType = "WorkspaceContentDeleted"
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
	// +kubebuilder:validation:Enum=""
	Target string `json:"target,omitempty"`
}

// ClusterWorkspaceList is a list of ClusterWorkspace resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspace `json:"items"`
}

// ClusterWorkspaceShard describes a Shard (== KCP instance) on which a number of
// workspaces will live
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.baseURL`,description="Type URL to directly connect to the shard"
// +kubebuilder:printcolumn:name="External URL",type=string,JSONPath=`.spec.externalURL`,description="The URL exposed in workspaces created on that shard"
type ClusterWorkspaceShard struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceShardSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceShardStatus `json:"status,omitempty"`
}

func (in *ClusterWorkspaceShard) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ClusterWorkspaceShard) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ClusterWorkspaceShard{}
var _ conditions.Setter = &ClusterWorkspaceShard{}

// ClusterWorkspaceShardSpec holds the desired state of the ClusterWorkspaceShard.
type ClusterWorkspaceShardSpec struct {
	// baseURL is the address of the KCP shard for direct connections, e.g. by some
	// front-proxy doing the fan-out to the shards.
	//
	// This will be defaulted to the shard's external address if not specified. Note that this
	// is only sensible in single-shards setups.
	//
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	// +optional
	BaseURL string `json:"baseURL"`

	// ExternalURL is the externally visible address presented to users in Workspace URLs.
	// Changing this will break all existing workspaces on that shard, i.e. existing
	// kubeconfigs of clients will be invalid. Hence, when changing this value, the old
	// URL used by clients must keep working.
	//
	// The external address will not be unique if a front-proxy does a fan-out to
	// shards, but all workspace client will talk to the front-proxy. In that case,
	// put the address of the front-proxy here.
	//
	// Note that movement of shards is only possible (in the future) between shards
	// that share a common external URL.
	//
	// This will be defaulted to the value of the baseURL.
	//
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:Required
	// +required
	ExternalURL string `json:"externalURL"`
}

// ClusterWorkspaceShardStatus communicates the observed state of the ClusterWorkspaceShard.
type ClusterWorkspaceShardStatus struct {
	// Set of integer resources that workspaces can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the ClusterWorkspaceShard.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ClusterWorkspaceShardList is a list of workspace shards
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspaceShard `json:"items"`
}

const (
	// ClusterWorkspacePhaseLabel holds the ClusterWorkspace.Status.Phase value, and is enforced to match
	// by a mutating admission webhook.
	ClusterWorkspacePhaseLabel = "internal.kcp.dev/phase"
	// ClusterWorkspaceInitializerLabelPrefix is the prefix for labels which match ClusterWorkspace.Status.Initializers,
	// and the set of labels with this prefix is enforced to match the set of initializers by a mutating admission
	// webhook.
	ClusterWorkspaceInitializerLabelPrefix = "initializer.internal.kcp.dev/"
)
