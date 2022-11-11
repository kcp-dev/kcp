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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

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
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=ws
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.URL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Workspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceSpec `json:"spec"`

	// +optional
	Status WorkspaceStatus `json:"status,omitempty"`
}

// WorkspaceSpec holds the desired state of the ClusterWorkspace.
type WorkspaceSpec struct {
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
	Type v1alpha1.ClusterWorkspaceTypeReference `json:"type,omitempty"`
}

// WorkspaceStatus communicates the observed state of the Workspace.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cluster) || has(self.cluster)",message="status.cluster is immutable"
type WorkspaceStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the workspace with standard Kubernetes
	// client libraries and command line tools.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// cluster is the name of the logical cluster this workspace is stored under.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="cluster is immutable"
	Cluster string `json:"cluster,omitempty"`

	// Phase of the workspace (Scheduling, Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase v1alpha1.WorkspacePhaseType `json:"phase,omitempty"`

	// Current processing state of the ClusterWorkspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// initializers must be cleared by a controller before the workspace is ready
	// and can be used.
	//
	// +optional
	Initializers []v1alpha1.ClusterWorkspaceInitializer `json:"initializers,omitempty"`
}

// WorkspaceList is a list of Workspaces
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Workspace `json:"items"`
}
