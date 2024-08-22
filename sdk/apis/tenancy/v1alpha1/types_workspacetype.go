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

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// WorkspaceTypeReservedNames defines the set of names that may not be
// used on user-supplied WorkspaceTypes.
// TODO(hasheddan): tie this definition of reserved names to the patches used to
// apply the same restrictions to the OpenAPISchema.
func WorkspaceTypeReservedNames() []string {
	return []string{
		"any",
		"system",
	}
}

// WorkspaceType specifies behaviour of workspaces of this type.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type WorkspaceType struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec WorkspaceTypeSpec `json:"spec,omitempty"`

	// +optional
	Status WorkspaceTypeStatus `json:"status,omitempty"`
}

type WorkspaceTypeSpec struct {
	// initializer determines if this WorkspaceType has an associated initializing
	// controller. These controllers are used to add functionality to a Workspace;
	// all controllers must finish their work before the Workspace becomes ready
	// for use.
	//
	// One initializing controller is supported per WorkspaceType; the identifier
	// for this initializer will be a colon-delimited string using the workspace in which
	// the WorkspaceType is defined, and the type's name. For example, if a
	// WorkspaceType `example` is created in the `root:org` workspace, the implicit
	// initializer name is `root:org:example`.
	//
	// +optional
	Initializer bool `json:"initializer,omitempty"`

	// extend is a list of other WorkspaceTypes whose initializers and limitAllowedChildren
	// and limitAllowedParents this WorkspaceType is inheriting. By (transitively) extending
	// another WorkspaceType, this WorkspaceType will be considered as that
	// other type in evaluation of limitAllowedChildren and limitAllowedParents constraints.
	//
	// A dependency cycle stop this WorkspaceType from being admitted as the type
	// of a Workspace.
	//
	// A non-existing dependency stop this WorkspaceType from being admitted as the type
	// of a Workspace.
	//
	// +optional
	Extend WorkspaceTypeExtension `json:"extend,omitempty"`

	// additionalWorkspaceLabels are a set of labels that will be added to a
	// Workspace on creation.
	//
	// +optional
	AdditionalWorkspaceLabels map[string]string `json:"additionalWorkspaceLabels,omitempty"`

	// defaultChildWorkspaceType is the WorkspaceType that will be used
	// by default if another, nested Workspace is created in a workspace
	// of this type. When this field is unset, the user must specify a type when
	// creating nested workspaces. Extending another WorkspaceType does
	// not inherit its defaultChildWorkspaceType.
	//
	// +optional
	DefaultChildWorkspaceType *WorkspaceTypeReference `json:"defaultChildWorkspaceType,omitempty"`

	// limitAllowedChildren specifies constraints for sub-workspaces created in workspaces
	// of this type. These are in addition to child constraints of types this one extends.
	//
	// +optional
	LimitAllowedChildren *WorkspaceTypeSelector `json:"limitAllowedChildren,omitempty"`

	// limitAllowedParents specifies constraints for the parent workspace that workspaces
	// of this type are created in. These are in addition to parent constraints of types this one
	// extends.
	//
	// +optional
	LimitAllowedParents *WorkspaceTypeSelector `json:"limitAllowedParents,omitempty"`

	// defaultAPIBindings are the APIs to bind during initialization of workspaces created from this type.
	// The APIBinding names will be generated dynamically.
	//
	// +optional
	DefaultAPIBindings []APIExportReference `json:"defaultAPIBindings,omitempty"`
}

// APIExportReference provides the fields necessary to resolve an APIExport.
type APIExportReference struct {
	// path is the fully-qualified path to the workspace containing the APIExport. If it is
	// empty, the current workspace is assumed.
	//
	// +optional
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`

	// export is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	Export string `json:"export"`
}

// WorkspaceTypeSelector describes a set of types.
type WorkspaceTypeSelector struct {
	// none means that no type matches.
	//
	// +kuberbuilders:Enum=true
	None bool `json:"none,omitempty"`

	// types is a list of WorkspaceTypes that match. A workspace type extending
	// another workspace type automatically is considered as that extended type as well
	// (even transitively).
	//
	// An empty list matches all types.
	//
	// +optional
	// +kubebuilder:validation:MinItems=1
	Types []WorkspaceTypeReference `json:"types,omitempty"`
}

// WorkspaceTypeExtension defines how other WorkspaceTypes are
// composed together to add functionality to the owning WorkspaceType.
type WorkspaceTypeExtension struct {
	// with are WorkspaceTypes whose initializers are added to the list
	// for the owning type, and for whom the owning type becomes an alias, as long
	// as all of their required types are not mentioned in without.
	//
	// +optional
	With []WorkspaceTypeReference `json:"with,omitempty"`
}

// These are valid conditions of WorkspaceType.
const (
	WorkspaceTypeVirtualWorkspaceURLsReady conditionsv1alpha1.ConditionType = "VirtualWorkspaceURLsReady"

	ErrorGeneratingURLsReason = "ErrorGeneratingURLs"
)

// WorkspaceTypeStatus defines the observed state of WorkspaceType.
type WorkspaceTypeStatus struct {
	// conditions is a list of conditions that apply to the APIExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// virtualWorkspaces contains all APIExport virtual workspace URLs.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

type VirtualWorkspace struct {
	// url is a WorkspaceType initialization virtual workspace URL.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

func (in *WorkspaceType) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *WorkspaceType) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// WorkspaceTypeList is a list of workspace types
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []WorkspaceType `json:"items"`
}

// WorkspaceAPIBindingsInitializer is a special-case initializer that waits for APIBindings defined
// on a WorkspaceType to be created.
const WorkspaceAPIBindingsInitializer corev1alpha1.LogicalClusterInitializer = "system:apibindings"

const (
	// WorkspacePhaseLabel holds the Workspace.Status.Phase value, and is enforced to match
	// by a mutating admission webhook.
	WorkspacePhaseLabel = "tenancy.kcp.io/phase"
	// WorkspaceInitializerLabelPrefix is the prefix for labels which match Workspace.Status.Initializers,
	// and the set of labels with this prefix is enforced to match the set of initializers by a mutating admission
	// webhook.
	WorkspaceInitializerLabelPrefix = "initializer.internal.kcp.io/"
)

const (
	// RootWorkspaceTypeName is a reference to the root logical cluster, which has no workspace type.
	RootWorkspaceTypeName = WorkspaceTypeName("root")
)

// ObjectName converts the proper name of a type that users interact with to the
// metadata.name of the WorkspaceType object.
func ObjectName(typeName WorkspaceTypeName) string {
	return string(typeName)
}

// TypeName converts the metadata.name of a WorkspaceType to the proper
// name of a type, as users interact with it.
func TypeName(objectName string) WorkspaceTypeName {
	return WorkspaceTypeName(objectName)
}
