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
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// RootCluster is the root of ClusterWorkspace based logical clusters.
var RootCluster = logicalcluster.Name("root")

// ClusterWorkspaceReservedNames defines the set of names that may not be used
// on user-supplied ClusterWorkspaces.
// TODO(hasheddan): tie this definition of reserved names to the patches used to
// apply the same restrictions to the OpenAPISchema.
func ClusterWorkspaceReservedNames() []string {
	return []string{
		"root",
		"system",
	}
}

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
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.baseURL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ClusterWorkspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceStatus `json:"status,omitempty"`
}

// ClusterWorkspaceSpec holds the desired state of the ClusterWorkspace.
type ClusterWorkspaceSpec struct {
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// type defines properties of the workspace both on creation (e.g. initial
	// resources and initially installed APIs) and during runtime (e.g. permissions).
	// If no type is provided, the default type for the workspace in which this workspace
	// is nesting will be used.
	//
	// The type is a reference to a ClusterWorkspaceType in the listed workspace, but
	// lower-cased. The ClusterWorkspaceType existence is validated at admission during
	// creation. The type is immutable after creation. The use of a type is gated via
	// the RBAC clusterworkspacetypes/use resource permission.
	//
	// +optional
	Type ClusterWorkspaceTypeReference `json:"type,omitempty"`

	// shard constraints onto which shards this cluster workspace can be scheduled to.
	// if the constraint is not fulfilled by the current location stored in the status,
	// movement will be attempted.
	//
	// Either shard name or shard selector must be specified.
	//
	// If the no shard constraints are specified, an arbitrary shard is chosen.
	//
	// +optional
	Shard *ShardConstraints `json:"shard,omitempty"`
}

type ShardConstraints struct {
	// name is the name of ClusterWorkspaceShard.
	//
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`

	// selector is a label selector that filters shard scheduling targets.
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ClusterWorkspaceTypeReference is a globally unique, fully qualified reference to a
// cluster workspace type.
type ClusterWorkspaceTypeReference struct {
	// name is the name of the ClusterWorkspaceType
	//
	// +required
	// +kubebuilder:validation:Required
	Name ClusterWorkspaceTypeName `json:"name"`

	// path is an absolute reference to the workspace that owns this type, e.g. root:org:ws.
	//
	// +optional
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`
}

// ClusterWorkspaceTypeName is a name of a ClusterWorkspaceType
//
// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?`
type ClusterWorkspaceTypeName string

func (r ClusterWorkspaceTypeReference) String() string {
	return fmt.Sprintf("%s:%s", r.Path, r.Name)
}

// ClusterWorkspaceStatus communicates the observed state of the ClusterWorkspace.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cluster) || has(self.cluster)",message="status.cluster is immutable"
type ClusterWorkspaceStatus struct {
	// Phase of the workspace (Scheduling / Initializing / Ready)
	//
	// +kubebuilder:default=Scheduling
	Phase WorkspacePhaseType `json:"phase,omitempty"`

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

	// cluster is the name of the logical cluster this workspace is stored under.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cluster is immutable"
	Cluster string `json:"cluster,omitempty"`

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
	Initializers []WorkspaceInitializer `json:"initializers,omitempty"`
}

// WorkspacePhaseType is the type of the current phase of the workspace
//
// +kubebuilder:validation:Enum=Scheduling;Initializing;Ready
type WorkspacePhaseType string

const (
	WorkspacePhaseScheduling   WorkspacePhaseType = "Scheduling"
	WorkspacePhaseInitializing WorkspacePhaseType = "Initializing"
	WorkspacePhaseReady        WorkspacePhaseType = "Ready"
)

const ExperimentalWorkspaceOwnerAnnotationKey string = "experimental.tenancy.kcp.dev/owner"

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

func (in *ClusterWorkspace) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ClusterWorkspace) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ClusterWorkspace{}
var _ conditions.Setter = &ClusterWorkspace{}

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

	// WorkspaceContentDeleted represents the status that all resources in the workspace are deleted.
	WorkspaceContentDeleted conditionsv1alpha1.ConditionType = "WorkspaceContentDeleted"

	// WorkspaceInitialized represents the status that initialization has finished.
	WorkspaceInitialized conditionsv1alpha1.ConditionType = "WorkspaceInitialized"
	// WorkspaceInitializedInitializerExists reason in WorkspaceInitialized condition means that there is at least
	// one initializer still left.
	WorkspaceInitializedInitializerExists = "InitializerExists"
	// WorkspaceInitializedWorkspaceDisappeared reason in WorkspaceInitialized condition means that the ThisWorkspace
	// object has disappeared.
	WorkspaceInitializedWorkspaceDisappeared = "WorkspaceDisappeared"

	// WorkspaceAPIBindingsInitialized represents the status of the initial APIBindings for the workspace.
	WorkspaceAPIBindingsInitialized conditionsv1alpha1.ConditionType = "APIBindingsInitialized"
	// WorkspaceInitializedWaitingOnAPIBindings is a reason for the APIBindingsInitialized condition that indicates
	// at least one APIBinding is not ready.
	WorkspaceInitializedWaitingOnAPIBindings = "WaitingOnAPIBindings"
	// WorkspaceInitializedClusterWorkspaceTypeInvalid is a reason for the APIBindingsInitialized
	// condition that indicates something is invalid with the ClusterWorkspaceType (e.g. a cycle trying
	// to resolve all the transitive types).
	WorkspaceInitializedClusterWorkspaceTypeInvalid = "ClusterWorkspaceTypeInvalid"
	// WorkspaceInitializedAPIBindingErrors is a reason for the APIBindingsInitialized condition that indicates there
	// were errors trying to initialize APIBindings for the workspace.
	WorkspaceInitializedAPIBindingErrors = "APIBindingErrors"
)

// ClusterWorkspaceList is a list of ClusterWorkspace resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspace `json:"items"`
}
