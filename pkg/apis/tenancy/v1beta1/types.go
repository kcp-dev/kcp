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
)

// Workspace defines a generic Kubernetes-cluster-like endpoint, with standard Kubernetes
// discovery APIs, OpenAPI and resource API endpoints.
//
// A workspace can be backed by different concrete types of workspace implementation,
// depending on access pattern. All of them have in common, that the resulting workspace under
// the URL where it is served can be used with standard Kubernetes API machinery and client
// libraries and command line tools.
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

	Spec WorkspaceSpec `json:"spec"`

	// +optional
	Status WorkspaceStatus `json:"status"`
}

// WorkspaceSpec holds the desired state of the ClusterWorkspace.
type WorkspaceSpec struct {
	// type defines properties of the workspace both on creation (e.g. initial
	// resources and initially installed APIs) and during runtime (e.g. permissions).
	//
	// The type is a reference to a ClusterWorkspaceType in the same workspace
	// with the same name, but lower-cased. The ClusterWorkspaceType existence is
	// validated during admission during creation, with the exception of the
	// "Universal" type whose existence is not required but respected if it exists.
	// The type is immutable after creation. The use of a type is gated via
	// the RBAC clusterworkspacetypes/use resource permission.
	//
	// +optional
	// +kubebuilder:default:="Universal"
	Type string `json:"type,omitempty"`
}

// WorkspaceStatus communicates the observed state of the Workspace.
type WorkspaceStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the workspace with standard Kubernetes
	// client libraries and command line tools.
	//
	// +required
	// +kubebuilder:format:uri
	URL string `json:"URL"`

	// Phase of the workspace (Initializing / Active / Terminating). This field is ALPHA.
	Phase v1alpha1.ClusterWorkspacePhaseType `json:"phase,omitempty"`
}

// WorkspaceList is a list of Workspaces
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Workspace `json:"items"`
}
