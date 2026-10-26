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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// APIIdentity is the view of one API identity: which APIExport owns it and
// which other APIExports hold it. It is computed from the APIExports of the
// installation and not stored anywhere; there is one per identity, named
// after the identity value, served read-only through the Admin workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.status.owner.name`,description="APIExport owning the identity"
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.status.owner.cluster`,description="Logical cluster of the owner"
// +kubebuilder:printcolumn:name="Core",type=boolean,JSONPath=`.status.core`,description="Identity of a core APIExport in root"
// +kubebuilder:printcolumn:name="Shared",type=string,JSONPath=`.status.conditions[?(@.type=="Shared")].status`,priority=1,description="True while other APIExports hold the identity"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIIdentity struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Status APIIdentityStatus `json:"status,omitempty"`
}

// ExportReference identifies an APIExport across the installation.
type ExportReference struct {
	// cluster is the logical cluster name hosting the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// name is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// uid is the UID of the APIExport, so that an export deleted and
	// recreated under the same name can be told apart from the original.
	//
	// +optional
	UID string `json:"uid,omitempty"`
}

// Equal reports whether two references name the same APIExport, ignoring
// the UID: a recreated export under the same name in the same cluster is
// the same export for the purpose of holding an identity.
func (r ExportReference) Equal(o ExportReference) bool {
	return r.Cluster == o.Cluster && r.Name == o.Name
}

// Holder is an APIExport, other than the owner, that serves the identity.
type Holder struct {
	ExportReference `json:",inline"`

	// since is when the holder was created.
	//
	// +optional
	Since metav1.Time `json:"since,omitempty"`
}

// APIIdentityStatus is the derived state of an identity. The object has no
// spec: nothing about it is user intent.
type APIIdentityStatus struct {
	// owner is the APIExport that holds the identity authoritatively: the
	// oldest one holding it. When it is deleted the oldest holder succeeds
	// it; an identity no APIExport holds any more has no entry.
	//
	// +required
	Owner ExportReference `json:"owner"`

	// holders lists the APIExports, other than the owner, that serve the
	// identity: those whose identity secret carries the same key, oldest
	// first.
	//
	// +optional
	// +listType=map
	// +listMapKey=cluster
	// +listMapKey=name
	Holders []Holder `json:"holders,omitempty"`

	// core is true for the identities of the core APIExports in the root
	// workspace, which every shard needs before it can serve.
	//
	// +optional
	Core bool `json:"core,omitempty"`

	// conditions is a list of conditions that apply to the identity.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

const (
	// APIIdentityOwnerLabel is the name of the APIExport that owns the
	// identity, so that entries can be selected by export name.
	APIIdentityOwnerLabel = "apiidentities.admin.kcp.io/owner"
	// APIIdentityOwnerClusterLabel is the logical cluster of the APIExport
	// that owns the identity, so that entries can be selected by workspace.
	APIIdentityOwnerClusterLabel = "apiidentities.admin.kcp.io/owner-cluster"
	// APIIdentityCoreLabel is "true" on the identities of the core
	// APIExports in root and "false" on every other one.
	APIIdentityCoreLabel = "apiidentities.admin.kcp.io/core"
)

// Labels returns the labels an entry carries for its owner and core flag.
func Labels(owner ExportReference, core bool) map[string]string {
	return map[string]string{
		APIIdentityOwnerLabel:        owner.Name,
		APIIdentityOwnerClusterLabel: owner.Cluster,
		APIIdentityCoreLabel:         strconv.FormatBool(core),
	}
}

const (
	// APIIdentityShared is True while holders is non-empty.
	APIIdentityShared conditionsv1alpha1.ConditionType = "Shared"
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
