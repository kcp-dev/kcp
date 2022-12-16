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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// These are valid conditions of APIExport.
const (
	APIExportIdentityValid conditionsv1alpha1.ConditionType = "IdentityValid"

	IdentityVerificationFailedReason = "IdentityVerificationFailed"
	IdentityGenerationFailedReason   = "IdentityGenerationFailed"

	APIExportVirtualWorkspaceURLsReady conditionsv1alpha1.ConditionType = "VirtualWorkspaceURLsReady"

	ErrorGeneratingURLsReason = "ErrorGeneratingURLs"
)

// These are for APIExport identity.
const (
	// SecretKeyAPIExportIdentity is the key in an identity secret for the identity of an APIExport.
	SecretKeyAPIExportIdentity = "key"
)

// APIExport registers an API and implementation to allow consumption by others
// through APIBindings.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIExport struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	//
	// +optional
	Spec APIExportSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	//
	// +optional
	Status APIExportStatus `json:"status,omitempty"`
}

const (
	// AnnotationAPIExportExtraKeyPrefix is the prefix of an annotation set on an APIExport to
	// provide extra info that will be made available to all APIBindings bound to this APIExport.
	// Any annotation with this prefix will be continuously synced to all the APIBindings bound to
	// this APIExport. If the annotation is removed from the APIExport, it will also be removed from
	// all APIBindings bound to this APIExport.
	AnnotationAPIExportExtraKeyPrefix = "extra.apis.kcp.dev/"
)

func (in *APIExport) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExport) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

const (
	// MaximalPermissionPolicyRBACUserGroupPrefix is the prefix for the user and group names
	// when verifying the APIExport.spec.maximalPermissionPolicy.
	MaximalPermissionPolicyRBACUserGroupPrefix = "apis.kcp.dev:binding:"
)

// APIExportSpec defines the desired state of APIExport.
type APIExportSpec struct {
	// latestResourceSchemas records the latest APIResourceSchemas that are exposed
	// with this APIExport.
	//
	// The schemas can be changed in the life-cycle of the APIExport. These changes
	// have no effect on existing APIBindings, but only on newly bound ones.
	//
	// For updating existing APIBindings, use an APIDeployment keeping bound
	// workspaces up-to-date.
	//
	// +optional
	// +listType=set
	LatestResourceSchemas []string `json:"latestResourceSchemas,omitempty"`

	// identity points to a secret that contains the API identity in the 'key' file.
	// The API identity determines an unique etcd prefix for objects stored via this
	// APIExport.
	//
	// Different APIExport in a workspace can share a common identity, or have different
	// ones. The identity (the secret) can also be transferred to another workspace
	// when the APIExport is moved.
	//
	// The identity is a secret of the API provider. The APIBindings referencing this APIExport
	// will store a derived, non-sensitive value of this identity.
	//
	// The identity of an APIExport cannot be changed. A derived, non-sensitive value of
	// the identity key is stored in the APIExport status and this value is immutable.
	//
	// The identity is defaulted. A secret with the name of the APIExport is automatically
	// created.
	//
	// +optional
	Identity *Identity `json:"identity,omitempty"`

	// TODO: before beta we should re-evaluate this field name

	// maximalPermissionPolicy will allow for a service provider to set an upper bound on what is allowed
	// for a consumer of this API. If the policy is not set, no upper bound is applied,
	// i.e the consuming users can do whatever the user workspace allows the user to do.
	//
	// The policy consists of RBAC (Cluster)Roles and (Cluster)Bindings. A request of a user in
	// a workspace that binds to this APIExport via an APIBinding is additionally checked against
	// these rules, with the user name and the groups prefixed with `apis.kcp.dev:binding:`.
	//
	// For example: assume a user `adam` with groups `system:authenticated` and `a-team` binds to
	// this APIExport in another workspace root:org:ws. Then a request in that workspace
	// against a resource of this APIExport is authorized as every other request in that workspace,
	// but in addition the RBAC policy here in the APIExport workspace has to grant access to the
	// user `apis.kcp.dev:binding:adam` with the groups `apis.kcp.dev:binding:system:authenticated`
	// and `apis.kcp.dev:binding:a-team`.
	//
	// +optional
	MaximalPermissionPolicy *MaximalPermissionPolicy `json:"maximalPermissionPolicy,omitempty"`

	// permissionClaims make resources available in APIExport's virtual workspace that are not part
	// of the actual APIExport resources.
	//
	// PermissionClaims are optional and should be the least access necessary to complete the functions
	// that the service provider needs. Access is asked for on a GroupResource + identity basis.
	//
	// PermissionClaims must be accepted by the user's explicit acknowledgement. Hence, when claims
	// change, the respecting objects are not visible immediately.
	//
	// PermissionClaims overlapping with the APIExport resources are ignored.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	PermissionClaims []PermissionClaim `json:"permissionClaims,omitempty"`
}

// Identity defines the identity of an APIExport, i.e. determines the etcd prefix
// data of this APIExport are stored under.
type Identity struct {
	// secretRef is a reference to a secret that contains the API identity in the 'key' file.
	//
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// MaximalPermissionPolicy is a wrapper type around the multiple options that would be allowed.
type MaximalPermissionPolicy struct {
	// local is the policy that is defined in same workspace as the API Export.
	// +optional
	Local *LocalAPIExportPolicy `json:"local,omitempty"`
}

// LocalAPIExportPolicy is a maximal permission policy
// that checks RBAC in the workspace of the API Export.
//
// In order to avoid conflicts the user and group name will be prefixed
// with "apis.kcp.dev:binding:".
type LocalAPIExportPolicy struct{}

const (
	APIExportPermissionClaimLabelPrefix = "claimed.internal.apis.kcp.dev/"
)

// PermissionClaim identifies an object by GR and identity hash.
// Its purpose is to determine the added permissions that a service provider may
// request and that a consumer may accept and allow the service provider access to.
//
// +kubebuilder:validation:XValidation:rule="(has(self.all) && self.all) != (has(self.resourceSelector) && size(self.resourceSelector) > 0)",message="either \"all\" or \"resourceSelector\" must be set"
// +kubebuilder:validation:XValidation:rule="!has(self.group) || self.group != \"tenancy.kcp.dev\" || self.resource != \"logicalclusters\" || (has(self.identityHash) && self.identityHash != \"\")",message="logicalclusters cannot be claimed"
type PermissionClaim struct {
	GroupResource `json:","`

	// all claims all resources for the given group/resource.
	// This is mutually exclusive with resourceSelector.
	// +optional
	All bool `json:"all,omitempty"`

	// resourceSelector is a list of claimed resource selectors.
	//
	// +optional
	ResourceSelector []ResourceSelector `json:"resourceSelector,omitempty"`

	// This is the identity for a given APIExport that the APIResourceSchema belongs to.
	// The hash can be found on APIExport and APIResourceSchema's status.
	// It will be empty for core types.
	// Note that one must look this up for a particular KCP instance.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.__namespace__) || has(self.name)",message="at least one field must be set"
type ResourceSelector struct {
	// name of an object within a claimed group/resource.
	// It matches the metadata.name field of the underlying object.
	// If namespace is unset, all objects matching that name will be claimed.
	//
	// +optional
	// +kubebuilder:validation:Pattern="^([a-z0-9][-a-z0-9_.]*)?[a-z0-9]$"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name,omitempty"`

	// namespace containing the named object. Matches metadata.namespace field.
	// If "name" is unset, all objects from the namespace are being claimed.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace,omitempty"`

	//
	// WARNING: If adding new fields, add them to the XValidation check!
	//
}

func (p PermissionClaim) String() string {
	// core resources have no group or identity hash
	if p.Group == "" {
		return p.Resource
	}
	if p.IdentityHash == "" {
		return fmt.Sprintf("%s.%s", p.Resource, p.Group)
	}
	return fmt.Sprintf("%s.%s:%s", p.Resource, p.Group, p.IdentityHash)
}

func (p PermissionClaim) Equal(claim PermissionClaim) bool {
	return p.Group == claim.Group &&
		p.Resource == claim.Resource &&
		p.IdentityHash == claim.IdentityHash
}

// GroupResource identifies a resource.
type GroupResource struct {
	// group is the name of an API group.
	// For core groups this is the empty string '""'.
	//
	// +kubebuilder:validation:Pattern=`^(|[a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?)$`
	// +optional
	Group string `json:"group,omitempty"`

	// resource is the name of the resource.
	// Note: it is worth noting that you can not ask for permissions for resource provided by a CRD
	// not provided by an api export.
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9]$`
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
}

// APIExportStatus defines the observed state of APIExport.
type APIExportStatus struct {
	// identityHash is the hash of the API identity key of this APIExport. This value
	// is immutable as soon as it is set.
	//
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// conditions is a list of conditions that apply to the APIExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// virtualWorkspaces contains all APIExport virtual workspace URLs.
	// This field will get deprecated in favor of APIExportEndpointSlice.
	//
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

type VirtualWorkspace struct {
	// url is an APIExport virtual workspace URL.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

// APIExportList is a list of APIExport resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExport `json:"items"`
}
