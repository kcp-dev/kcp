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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// WorkspaceTypeReference is a globally unique, fully qualified reference to a workspace type.
type WorkspaceTypeReference struct {
	// name is the name of the WorkspaceType
	//
	// +required
	// +kubebuilder:validation:Required
	Name WorkspaceTypeName `json:"name"`

	// path is an absolute reference to the workspace that owns this type, e.g. root:org:ws.
	//
	// +optional
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`
}

// WorkspaceTypeName is a name of a WorkspaceType
//
// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?`
type WorkspaceTypeName string

func (r WorkspaceTypeReference) String() string {
	if r.Path == "" {
		return string(r.Name)
	}
	return fmt.Sprintf("%s:%s", r.Path, r.Name)
}

const ExperimentalWorkspaceOwnerAnnotationKey string = "experimental.tenancy.kcp.io/owner"

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
	// WorkspaceInitializedWorkspaceDisappeared reason in WorkspaceInitialized condition means that the LogicalCluster
	// object has disappeared.
	WorkspaceInitializedWorkspaceDisappeared = "WorkspaceDisappeared"

	// WorkspaceAPIBindingsInitialized represents the status of the initial APIBindings for the workspace.
	WorkspaceAPIBindingsInitialized conditionsv1alpha1.ConditionType = "APIBindingsInitialized"
	// WorkspaceInitializedWaitingOnAPIBindings is a reason for the APIBindingsInitialized condition that indicates
	// at least one APIBinding is not ready.
	WorkspaceInitializedWaitingOnAPIBindings = "WaitingOnAPIBindings"
	// WorkspaceInitializedWorkspaceTypeInvalid is a reason for the APIBindingsInitialized
	// condition that indicates something is invalid with the WorkspaceType (e.g. a cycle trying
	// to resolve all the transitive types).
	WorkspaceInitializedWorkspaceTypeInvalid = "WorkspaceTypesInvalid"
	// WorkspaceInitializedAPIBindingErrors is a reason for the APIBindingsInitialized condition that indicates there
	// were errors trying to initialize APIBindings for the workspace.
	WorkspaceInitializedAPIBindingErrors = "APIBindingErrors"
)

// LogicalClusterTypeAnnotationKey is the annotation key used to indicate
// the type of the workspace on the corresponding LogicalCluster object. Its format is "root:ws:name".
const LogicalClusterTypeAnnotationKey = "internal.tenancy.kcp.io/type"

// Workspace defines a generic Kubernetes-cluster-like endpoint, with standard Kubernetes
// discovery APIs, OpenAPI and resource API endpoints.
//
// A workspace can be backed by different concrete types of workspace implementation,
// depending on access pattern. All workspace implementations share the characteristic
// that the URL that serves a given workspace can be used with standard Kubernetes
// API machinery and client libraries and command line tools.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=ws
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.metadata.labels['region']`,description="The region this workspace is in"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.metadata.labels['tenancy\.kcp\.io/phase']`,description="The current phase (e.g. Scheduling, Initializing, Ready, Deleting)"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.URL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Workspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceSpec `json:"spec"`

	// +optional
	Status WorkspaceStatus `json:"status,omitempty"`
}

// WorkspaceSpec holds the desired state of the Workspace.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.URL) || has(self.URL)",message="URL cannot be unset"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cluster) || has(self.cluster)",message="cluster cannot be unset"
type WorkspaceSpec struct {
	// type defines properties of the workspace both on creation (e.g. initial
	// resources and initially installed APIs) and during runtime (e.g. permissions).
	// If no type is provided, the default type for the workspace in which this workspace
	// is nesting will be used.
	//
	// The type is a reference to a WorkspaceType in the listed workspace, but
	// lower-cased. The WorkspaceType existence is validated at admission during
	// creation. The type is immutable after creation. The use of a type is gated via
	// the RBAC workspacetypes/use resource permission.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.name == oldSelf.name",message="name is immutable"
	// +kubebuilder:validation:XValidation:rule="has(oldSelf.path) == has(self.path)",message="path is immutable"
	// +kubebuilder:validation:XValidation:rule="!has(oldSelf.path) || !has(self.path) || self.path == oldSelf.path",message="path is immutable"
	Type WorkspaceTypeReference `json:"type,omitempty"`

	// location constraints where this workspace can be scheduled to.
	//
	// If the no location is specified, an arbitrary location is chosen.
	//
	// +optional
	Location *WorkspaceLocation `json:"location,omitempty"`

	// cluster is the name of the logical cluster this workspace is stored under.
	//
	// Set by the system.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cluster is immutable"
	Cluster string `json:"cluster,omitempty"`

	// URL is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the workspace with standard Kubernetes
	// client libraries and command line tools.
	//
	// Set by the system.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`
}

type WorkspaceLocation struct {

	// selector is a label selector that filters workspace scheduling targets.
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// WorkspaceStatus communicates the observed state of the Workspace.
type WorkspaceStatus struct {
	// Phase of the workspace (Scheduling, Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase corev1alpha1.LogicalClusterPhaseType `json:"phase,omitempty"`

	// Current processing state of the Workspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// initializers must be cleared by a controller before the workspace is ready
	// and can be used.
	//
	// +optional
	Initializers []corev1alpha1.LogicalClusterInitializer `json:"initializers,omitempty"`
}

func (in *Workspace) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *Workspace) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

// WorkspaceList is a list of Workspaces
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Workspace `json:"items"`
}
