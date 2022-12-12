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
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// ClusterWorkspaceTypeReservedNames defines the set of names that may not be
// used on user-supplied ClusterWorkspaceTypes.
// TODO(hasheddan): tie this definition of reserved names to the patches used to
// apply the same restrictions to the OpenAPISchema.
func ClusterWorkspaceTypeReservedNames() []string {
	return []string{
		"any",
		"system",
	}
}

// ClusterWorkspaceType specifies behaviour of workspaces of this type.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ClusterWorkspaceType struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceTypeSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceTypeStatus `json:"status,omitempty"`
}

type ClusterWorkspaceTypeSpec struct {
	// initializer determines if this ClusterWorkspaceType has an associated initializing
	// controller. These controllers are used to add functionality to a ClusterWorkspace;
	// all controllers must finish their work before the ClusterWorkspace becomes ready
	// for use.
	//
	// One initializing controller is supported per ClusterWorkspaceType; the identifier
	// for this initializer will be a colon-delimited string using the workspace in which
	// the ClusterWorkspaceType is defined, and the type's name. For example, if a
	// ClusterWorkspaceType `example` is created in the `root:org` workspace, the implicit
	// initializer name is `root:org:Example`.
	//
	// +optional
	Initializer bool `json:"initializer,omitempty"`

	// extend is a list of other ClusterWorkspaceTypes whose initializers and limitAllowedChildren
	// and limitAllowedParents this ClusterWorkspaceType is inheriting. By (transitively) extending
	// another ClusterWorkspaceType, this ClusterWorkspaceType will be considered as that
	// other type in evaluation of limitAllowedChildren and limitAllowedParents constraints.
	//
	// A dependency cycle stop this ClusterWorkspaceType from being admitted as the type
	// of a ClusterWorkspace.
	//
	// A non-existing dependency stop this ClusterWorkspaceType from being admitted as the type
	// of a ClusterWorkspace.
	//
	// +optional
	Extend ClusterWorkspaceTypeExtension `json:"extend,omitempty"`

	// additionalWorkspaceLabels are a set of labels that will be added to a
	// ClusterWorkspace on creation.
	//
	// +optional
	AdditionalWorkspaceLabels map[string]string `json:"additionalWorkspaceLabels,omitempty"`

	// defaultChildWorkspaceType is the ClusterWorkspaceType that will be used
	// by default if another, nested ClusterWorkspace is created in a workspace
	// of this type. When this field is unset, the user must specify a type when
	// creating nested workspaces. Extending another ClusterWorkspaceType does
	// not inherit its defaultChildWorkspaceType.
	//
	// +optional
	DefaultChildWorkspaceType *ClusterWorkspaceTypeReference `json:"defaultChildWorkspaceType,omitempty"`

	// limitAllowedChildren specifies constraints for sub-workspaces created in workspaces
	// of this type. These are in addition to child constraints of types this one extends.
	//
	// +optional
	LimitAllowedChildren *ClusterWorkspaceTypeSelector `json:"limitAllowedChildren,omitempty"`

	// limitAllowedParents specifies constraints for the parent workspace that workspaces
	// of this type are created in. These are in addition to parent constraints of types this one
	// extends.
	//
	// +optional
	LimitAllowedParents *ClusterWorkspaceTypeSelector `json:"limitAllowedParents,omitempty"`

	// defaultAPIBindings are the APIs to bind during initialization of workspaces created from this type.
	// The APIBinding names will be generated dynamically.
	//
	// +optional
	// +listType=map
	// +listMapKey=path
	// +listMapKey=export
	DefaultAPIBindings []APIExportReference `json:"defaultAPIBindings,omitempty"`
}

// APIExportReference provides the fields necessary to resolve an APIExport.
type APIExportReference struct {
	// path is the fully-qualified path to the workspace containing the APIExport.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`

	// export is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	Export string `json:"export"`
}

// ClusterWorkspaceTypeSelector describes a set of types.
type ClusterWorkspaceTypeSelector struct {
	// none means that no type matches.
	//
	// +kuberbuilders:Enum=true
	None bool `json:"none,omitempty"`

	// types is a list of ClusterWorkspaceTypes that match. A workspace type extending
	// another workspace type automatically is considered as that extended type as well
	// (even transitively).
	//
	// An empty list matches all types.
	//
	// +optional
	// +kubebuilder:validation:MinItems=1
	Types []ClusterWorkspaceTypeReference `json:"types,omitempty"`
}

// ClusterWorkspaceTypeExtension defines how other ClusterWorkspaceTypes are
// composed together to add functionality to the owning ClusterWorkspaceType.
type ClusterWorkspaceTypeExtension struct {
	// with are ClusterWorkspaceTypes whose initializers are added to the list
	// for the owning type, and for whom the owning type becomes an alias, as long
	// as all of their required types are not mentioned in without.
	//
	// +optional
	With []ClusterWorkspaceTypeReference `json:"with,omitempty"`
}

// These are valid conditions of ClusterWorkspaceType.
const (
	ClusterWorkspaceTypeVirtualWorkspaceURLsReady conditionsv1alpha1.ConditionType = "VirtualWorkspaceURLsReady"
	ErrorGeneratingURLsReason                                                      = "ErrorGeneratingURLs"
)

// ClusterWorkspaceTypeStatus defines the observed state of ClusterWorkspaceType.
type ClusterWorkspaceTypeStatus struct {
	// conditions is a list of conditions that apply to the APIExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// virtualWorkspaces contains all APIExport virtual workspace URLs.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

type VirtualWorkspace struct {
	// url is a ClusterWorkspaceType initialization virtual workspace URL.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

func (in *ClusterWorkspaceType) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *ClusterWorkspaceType) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// ClusterWorkspaceTypeList is a list of cluster workspace types
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspaceType `json:"items"`
}

// WorkspaceInitializer is a unique string corresponding to a cluster workspace
// initialization controller for the given type of workspaces.
//
// +kubebuilder:validation:Pattern:="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*(:[a-z0-9][a-z0-9]([-a-z0-9]*[a-z0-9])?))|(system:.+)$"
type WorkspaceInitializer string

// WorkspaceAPIBindingsInitializer is a special-case initializer that waits for APIBindings defined
// on a ClusterWorkspaceType to be created.
const WorkspaceAPIBindingsInitializer WorkspaceInitializer = "system:apibindings"

const (
	// WorkspacePhaseLabel holds the ClusterWorkspace.Status.Phase value, and is enforced to match
	// by a mutating admission webhook.
	WorkspacePhaseLabel = "tenancy.kcp.dev/phase"
	// WorkspaceInitializerLabelPrefix is the prefix for labels which match ClusterWorkspace.Status.Initializers,
	// and the set of labels with this prefix is enforced to match the set of initializers by a mutating admission
	// webhook.
	WorkspaceInitializerLabelPrefix = "initializer.internal.kcp.dev/"
)

const (
	// RootWorkspaceTypeName is a reference to the root logical cluster, which has no cluster workspace type
	RootWorkspaceTypeName = ClusterWorkspaceTypeName("root")
)

var (
	// RootWorkspaceTypeReference is a reference to the root logical cluster, which has no cluster workspace type
	RootWorkspaceTypeReference = ClusterWorkspaceTypeReference{
		Name: RootWorkspaceTypeName,
		Path: RootCluster.String(),
	}

	// RootWorkspaceType is the implicit type of the root logical cluster.
	RootWorkspaceType = &ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: ObjectName(RootWorkspaceTypeReference.Name),
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: RootWorkspaceTypeReference.Path,
			},
		},
		Spec: ClusterWorkspaceTypeSpec{
			LimitAllowedParents: &ClusterWorkspaceTypeSelector{
				None: true,
			},
		},
	}
)

// ObjectName converts the proper name of a type that users interact with to the
// metadata.name of the ClusterWorkspaceType object.
func ObjectName(typeName ClusterWorkspaceTypeName) string {
	return string(typeName)
}

// TypeName converts the metadata.name of a ClusterWorkspaceType to the proper
// name of a type, as users interact with it.
func TypeName(objectName string) ClusterWorkspaceTypeName {
	return ClusterWorkspaceTypeName(objectName)
}
