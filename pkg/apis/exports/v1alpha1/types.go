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
)

// Export is a reference to an object in a workspace to be exposed to workspaces
// below. E.g.
// - an Export in the root workspace references any object in any workspace and
//   exposes it to all other workspaces.
// - an Export in an org workspace references any object in the same org and
//   exposes it to all other workspaces in the org.
// - an Export in a non-org, non-root workspace references any object in the same
//   workspace and exposes it to the same workspace.
//
// An export can be bound by binding objects in the workspaces that the Export
// is exposed to. A binding object is identified by its name and its fully
// qualified workspace name (e.g. root, root:org or org:workspace).
//
// Binding to an export is only protected by RBAC in the same workspace the
// Export exists in through the exports resource and bind verb. During binding
// this permission is checked through a SubjectAccessReview against that workspace.
// There is no need for a binding to be able to access the export object directly
// in any way.
//
// The name of the export must be of the shape `idenfier.resource.group` where
// identifier is a valid DNS 1123 segment, and resource and group match the
// values in spec.reference.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Resource",type="string",JSONPath=`.reference.resource`,priority=1
// +kubebuilder:printcolumn:name="Workspace",type="string",JSONPath=`.reference.workspace`,priority=2
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=`.reference.name`,priority=3
type Export struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +required
	Reference ExportReference `json:"reference"`
}

// ExportReference specified the referenced object.
type ExportReference struct {
	// group is the group of the referenced object.
	//
	// kubebuilder:validation:MinLength=1
	// +required
	Group string `json:"group"`

	// resource is the resource of the referenced object.
	//
	// kubebuilder:validation:MinLength=1
	// +required
	Resource string `json:"resource"`

	// workspace is the workspace the referenced object is in.
	//
	// If the workspace is empty, the referenced object is in the same workspace.
	//
	// kubebuilder:validation:MinLength=1
	Workspace string `json:"workspace"`

	// name is the name of the referenced object in the given workspace.
	//
	// The name is defaulted to the first segment of the object's name.
	//
	// +required
	// kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ExportBindingReference specified the referenced object to be embedded
// into bindings.
type ExportBindingReference struct {
	// local indicates an Export in the same workspace.
	//
	// +optional
	Local bool `json:"local,omitempty"`

	// workspace is the workspace the referenced object is in within the same
	// organization.
	//
	// +optional
	Workspace string `json:"workspace,omitempty"`

	// organization indicates an Export in the organization workspace.
	//
	// +optional
	Organization bool `json:"organization,omitempty"`

	// global indicates an Export in the root workspace.
	//
	// +optional
	Global bool `json:"global,omitempty"`

	// name is the name of the referenced object in the given workspace.
	// +required
	// kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ExportList is a list of Export resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Export `json:"items"`
}
