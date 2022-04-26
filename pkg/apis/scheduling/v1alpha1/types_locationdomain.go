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

// LocationDomain defines a set of locations of a certain type sharing uniform properties of some kind.
//
// In case of Workload locations this includes
// - a common set of supported APIs
// - a common software defined networking layer for communication between the locations.
// - ability to reschedule workloads to a new location is few or no disruption of a workload.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`,description="Type of the workspace"
type LocationDomain struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec LocationDomainSpec `json:"spec,omitempty"`

	// +optional
	Status LocationDomainStatus `json:"status,omitempty"`
}

// LocationDomainSpec holds the desired state of the location domain.
type LocationDomainSpec struct {
	// instances is a references to instance objects subject to this location domain. Depending on type
	// this will usually be a workspace reference.
	//
	// +required
	// +kubebuilder:Required
	Instances InstancesReference `json:"instances"`

	// workspaceSelector defines which workspaces (labels) this domain automatically is assigned to.
	//
	// +optional
	WorkspaceSelector *WorkspaceSelector `json:"workspaceSelector,omitempty"`
}

// InstancesReference describes a reference to a workspace holding the instances
// subject to the location domain. Exactly one of the fields must be set.
type InstancesReference struct {
	// workspace is a reference to a workspace with instances subject to the location
	// domain. Exactly one must be set.
	//
	// +optional
	Workspace *WorkspaceExportReference `json:"workspace,omitempty"`
}

// WorkspaceExportReference describes an API and backing implementation that are provided by an actor in the
// specified Workspace.
type WorkspaceExportReference struct {
	// name is a workspace name meaning a workspace defined in the workspace this LocationDomain is defined it.
	//
	// +required
	// +kubebuilder:validation:Required
	WorkspaceName WorkspaceName `json:"name"`
}

// WorkspaceName is a non-absolute workspace name, e.g. "workspace", but not "root:company:workspace".
//
// +kube:validation:MinLength=1
// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*[a-z0-9]$`
type WorkspaceName string

// WorkspaceSelector selects a subset of workspaces this location domain applies to by default.
type WorkspaceSelector struct {
	// Types is a list of workspace types that matches this location.
	// Empty list means that this location domain does not participate in automatic assignment.
	//
	// +required
	// +kubebuilder:Required
	// +kube:validation:MinItems=1
	Types []WorkspaceType `json:"types"`

	// priority is used when multiple LocationDomains and their workspace selector match. The largest number wins.
	//
	// A negative priority means that this location is not used for any automatic assignment.
	//
	// +kubebuilder:Default=0
	// +optional
	Priority int32 `json:"priority"`
}

// WorkspaceType is a case-insensitive ClusterWorkspaceType name to be used in Workspace.spec.type.
//
// +kube:validation:MinLength=1
// +kubebuilder:validation:Pattern=`^[a-zA-Z][A-Za-z0-9-]*[a-zA-Z0-9]$`
type WorkspaceType string

// LocationDomainStatus defines the observed state of Location.
type LocationDomainStatus struct {
	// conditions is a list of conditions that apply to the LocationDomain.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *LocationDomain) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *LocationDomain) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// LocationDomainList is a list of location domains.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LocationDomainList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []LocationDomain `json:"items"`
}
