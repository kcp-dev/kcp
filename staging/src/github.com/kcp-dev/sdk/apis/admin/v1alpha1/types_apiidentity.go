/*
Copyright 2026 The kcp Authors.

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

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// APIIdentity is the installation-wide record of one APIExport or
// ClusterCachedResource identity: who owns it, what is served under it, who
// else stores on its prefix, and, for identities that were shared by several
// exports before identities became unique, how the conversion is going.
//
// The object is named after the identity value. It is derived from the
// APIExports by the shard that hosts the owning export, written into that
// shard's shard-local system:shard logical cluster, replicated to the cache
// server and visible for the whole installation in the Admin workspace
// (kubectl ws use :admin). It outlives its owner: when the owner is deleted
// the object stays, Orphaned, until no binding stores under the identity
// any more, so that no other export can take the identity over while
// consumers' data still sits under it.
//
// Everything but spec is derived. spec holds the few operator knobs of the
// legacy conversion.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.status.owner.export`,description="The owning export"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.owner.cluster`,description="The owning export's logical cluster"
// +kubebuilder:printcolumn:name="Core",type=boolean,JSONPath=`.status.core`
// +kubebuilder:printcolumn:name="Shared",type=string,JSONPath=`.status.conditions[?(@.type=="StorageShared")].status`
// +kubebuilder:printcolumn:name="Legacy",type=string,JSONPath=`.status.conditions[?(@.type=="LegacyShared")].status`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIIdentity struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec APIIdentitySpec `json:"spec,omitempty"`

	// +optional
	Status APIIdentityStatus `json:"status,omitempty"`
}

// APIIdentitySpec holds the operator knobs of the legacy conversion.
type APIIdentitySpec struct {
	// owner selects which holder of a legacy shared identity keeps it when
	// the identity is converted; every other holder gets a fresh identity.
	// Defaults to the oldest holder. Settable before conversion; ignored
	// afterwards.
	//
	// +optional
	Owner *ExportIdentityReference `json:"owner,omitempty"`

	// convert holds the conversion of a legacy shared identity when false.
	// Defaults to true. A held identity stays grandfathered: every holder
	// keeps the identity and nothing changes.
	//
	// +optional
	Convert *bool `json:"convert,omitempty"`

	// retireAliases retires every alias of a converted identity at once
	// instead of waiting for references to go away.
	//
	// +optional
	RetireAliases bool `json:"retireAliases,omitempty"`
}

// ExportIdentityReference names an APIExport by logical cluster and name.
type ExportIdentityReference struct {
	// cluster is the logical cluster name the export lives in.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// export is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Export string `json:"export"`
}

// StorageShare records an export whose bindings sit on this identity's
// prefix for some resources.
type StorageShare struct {
	// export is the export storing on this identity's prefix.
	//
	// +required
	Export ExportIdentityReference `json:"export"`

	// resources lists the group/resources it stores there.
	//
	// +optional
	// +listType=atomic
	Resources []apisv1alpha2.GroupResource `json:"resources,omitempty"`
}

// IdentityAlias records a former identity still honored for some resources.
type IdentityAlias struct {
	// identity is the former identity.
	//
	// +required
	Identity string `json:"identity"`

	// groupResources lists the resources the alias applies to.
	//
	// +optional
	// +listType=atomic
	GroupResources []apisv1alpha2.GroupResource `json:"groupResources,omitempty"`

	// since is when the alias was created.
	//
	// +optional
	Since *metav1.Time `json:"since,omitempty"`

	// references counts the permission claims that still name the alias for
	// one of the resources.
	//
	// +optional
	References int32 `json:"references,omitempty"`
}

// APIIdentityStatus is derived from the APIExports.
type APIIdentityStatus struct {
	// owner is the export holding the identity. Empty when the identity is
	// orphaned.
	//
	// +optional
	Owner *ExportIdentityReference `json:"owner,omitempty"`

	// groupResources lists the resources served under this identity.
	//
	// +optional
	// +listType=atomic
	GroupResources []apisv1alpha2.GroupResource `json:"groupResources,omitempty"`

	// storageSharedWith lists other exports storing on this identity's
	// prefix.
	//
	// +optional
	// +listType=atomic
	StorageSharedWith []StorageShare `json:"storageSharedWith,omitempty"`

	// core is true for the identity of one of kcp's core exports in the root
	// workspace.
	//
	// +optional
	Core bool `json:"core,omitempty"`

	// legacyHolders lists every export holding this identity while it is
	// shared. Set only for identities shared before identities became unique.
	//
	// +optional
	// +listType=atomic
	LegacyHolders []ExportIdentityReference `json:"legacyHolders,omitempty"`

	// aliases lists former identities of the owner that are still honored for
	// the owner's resources.
	//
	// +optional
	// +listType=atomic
	Aliases []IdentityAlias `json:"aliases,omitempty"`

	// conditions is a list of conditions that apply to the APIIdentity.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

const (
	// APIIdentityStorageShared is True when storageSharedWith is non-empty.
	APIIdentityStorageShared conditionsv1alpha1.ConditionType = "StorageShared"
	// APIIdentityOrphaned is True when no export holds the identity any more
	// while bindings may still store under it.
	APIIdentityOrphaned conditionsv1alpha1.ConditionType = "Orphaned"
	// APIIdentityLegacyShared is True while several exports hold the
	// identity. Reason APIIdentityReasonConversionHeld when spec.convert is
	// false, APIIdentityReasonAmbiguousResource when two holders serve the
	// same resource.
	APIIdentityLegacyShared conditionsv1alpha1.ConditionType = "LegacyShared"
	// APIIdentityAliased is True while any alias exists.
	APIIdentityAliased conditionsv1alpha1.ConditionType = "Aliased"

	APIIdentityReasonConversionHeld    = "ConversionHeld"
	APIIdentityReasonAmbiguousResource = "AmbiguousResource"
	APIIdentityReasonConverting        = "Converting"
)

func (in *APIIdentity) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *APIIdentity) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &APIIdentity{}
var _ conditions.Setter = &APIIdentity{}

// APIIdentityList is a list of APIIdentity resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIIdentity `json:"items"`
}
