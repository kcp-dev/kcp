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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

const (
	// ThisWorkspaceName is the name of the ThisWorkspace singleton.
	ThisWorkspaceName = "this"

	// ThisWorkspaceFinalizer attached to new ClusterWorkspace (in phase ClusterWorkspacePhaseScheduling) resources so that we can control
	// deletion of ThisWorkspace resources
	ThisWorkspaceFinalizer = "tenancy.kcp.dev/thisworkspace"

	// ThisWorkspaceTypeAnnotationKey is the annotation key used to indicate
	// the type of the workspace on the ThisWorkspace object. Its format is "root:ws:name".
	ThisWorkspaceTypeAnnotationKey = "internal.tenancy.kcp.dev/type"
)

// ThisWorkspace describes the current workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.URL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ThisWorkspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec ThisWorkspaceSpec `json:"spec,omitempty"`
	// +optional
	Status ThisWorkspaceStatus `json:"status,omitempty"`
}

// ThisWorkspaceSpec is the specification of the ThisWorkspace resource.
type ThisWorkspaceSpec struct {
	// DirectlyDeletable indicates that this workspace can be directly deleted by the user
	// from within the workspace.
	//
	// +optional
	// +kubebuilder:default=false
	DirectlyDeletable bool `json:"directlyDeletable,omitempty"`

	// owner is a reference to a resource controlling the life-cycle of this workspace.
	// On deletion of the ThisWorkspace, the finalizer tenancy.kcp.dev/thisworkspace is
	// removed from the owner.
	//
	// When this object is deleted, but the owner is not deleted, the owner is deleted
	// too.
	//
	// +optional
	Owner *ThisWorkspaceOwner `json:"owner,omitempty"`

	// initializers are set on creation by the system and copied to status when
	// initialization starts.
	//
	// +optional
	Initializers []ClusterWorkspaceInitializer `json:"initializers,omitempty"`
}

// ThisWorkspaceOwner is a reference to a resource controlling the life-cycle of a ThisWorkspace.
type ThisWorkspaceOwner struct {
	// apiVersion is the group and API version of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([^/]+/)?[^/]+$`
	APIVersion string `json:"apiVersion"`

	// resource is API resource to access the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// name is the name of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// namespace is the optional namespace of the owner.
	//
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// cluster is the logical cluster in which the owner is located.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// UID is the UID of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	UID types.UID `json:"uid"`
}

// ThisWorkspaceStatus communicates the observed state of the Workspace.
type ThisWorkspaceStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the workspace with standard Kubernetes
	// client libraries and command line tools.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// Phase of the workspace (Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase ClusterWorkspacePhaseType `json:"phase,omitempty"`

	// Current processing state of the ThisWorkspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// initializers are set on creation by the system and must be cleared
	// by a controller before the workspace can be used. The ThisWorkspace will
	// stay in the phase "Initializing" state until all initializers are cleared.
	//
	// +optional
	Initializers []ClusterWorkspaceInitializer `json:"initializers,omitempty"`
}

func (in *ThisWorkspace) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ThisWorkspace) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ThisWorkspace{}
var _ conditions.Setter = &ThisWorkspace{}

// ThisWorkspaceList is a list of ThisWorkspaces
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ThisWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ThisWorkspace `json:"items"`
}
