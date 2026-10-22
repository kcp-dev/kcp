/*
Copyright 2025 The kcp Authors.

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

package v1alpha2

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/sdk/apis/apis"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

const (
	// APIExportEndpointSliceSkipAnnotation is an annotation that can be set on an APIExport to skip the creation of default APIExportEndpointSlice.
	APIExportEndpointSliceSkipAnnotation = "apiexports.apis.kcp.io/skip-endpointslice"
)

// These are valid conditions of APIExport.
const (
	APIExportIdentityValid conditionsv1alpha1.ConditionType = "IdentityValid"

	IdentityVerificationFailedReason = "IdentityVerificationFailed"
	IdentityGenerationFailedReason   = "IdentityGenerationFailed"

	// IdentityShared is an informational condition set when this export's
	// identity secret is referenced by more than one APIExport, listing the
	// co-owners in the message.
	IdentityShared conditionsv1alpha1.ConditionType = "IdentityShared"

	// ClaimIdentityAliased is an informational condition set on a claiming
	// APIExport whose permission claims reference an alias identity hash of
	// another export; the message names the canonical hash to migrate to.
	// Aliases are retired after a deprecation window, so the reference has
	// to be updated.
	ClaimIdentityAliased conditionsv1alpha1.ConditionType = "ClaimIdentityAliased"

	// ClaimsResolved is True when every permission claim of the export that
	// names no identity could be resolved to exactly one export, by export
	// reference or by grant. It is False with reason ClaimUnresolvedReason or
	// ClaimAmbiguousReason otherwise; unresolved claims are inert.
	ClaimsResolved conditionsv1alpha1.ConditionType = "ClaimsResolved"

	ClaimUnresolvedReason = "ClaimUnresolved"
	ClaimAmbiguousReason  = "ClaimAmbiguous"

	// StorageShared is True when some resource of this export is stored on a
	// prefix that is not this export's identity (adopted storage), or when
	// another export's bindings sit on this export's prefix (sharedWith).
	StorageShared conditionsv1alpha1.ConditionType = "StorageShared"

	// IdentityLegacyShared is True while this export shares its identity with
	// other exports, which was possible by sharing an identity secret. Such
	// identities are converted once: every holder but the owner gets a fresh
	// identity, with the old one kept as an alias for a deprecation window.
	IdentityLegacyShared conditionsv1alpha1.ConditionType = "IdentityLegacyShared"

	// ResourcesValid is True when every resource of the export could be
	// resolved: adopted resources found their source, its consent and its
	// schema.
	ResourcesValid conditionsv1alpha1.ConditionType = "ResourcesValid"

	StorageNotSharedReason     = "StorageNotShared"
	StorageSourceInvalidReason = "StorageSourceInvalid"

	APIExportVirtualWorkspaceURLsReady conditionsv1alpha1.ConditionType = "VirtualWorkspaceURLsReady"

	ErrorGeneratingURLsReason = "ErrorGeneratingURLs"

	APIExportPermissionClaimLabelPrefix = "claimed.internal.apis.kcp.io/"
)

// APIExport registers an API and implementation to allow consumption by others
// through APIBindings.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
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

func (in *APIExport) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExport) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// APIExportSpec defines the desired state of APIExport.
type APIExportSpec struct {
	// Resources records the APIResourceSchemas that are exposed with this
	// APIExport.
	//
	// The schemas can be changed in the life-cycle of the APIExport. These changes
	// have no effect on existing APIBindings, but only on newly bound ones.
	//
	// For updating existing APIBindings, use an APIDeployment keeping bound
	// workspaces up-to-date.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +listMapKey=group
	Resources []ResourceSchema `json:"resources,omitempty"`

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
	// these rules, with the user name and the groups prefixed with `apis.kcp.io:binding:`.
	//
	// For example: assume a user `adam` with groups `system:authenticated` and `a-team` binds to
	// this APIExport in another workspace root:org:ws. Then a request in that workspace
	// against a resource of this APIExport is authorized as every other request in that workspace,
	// but in addition the RBAC policy here in the APIExport workspace has to grant access to the
	// user `apis.kcp.io:binding:adam` with the groups `apis.kcp.io:binding:system:authenticated`
	// and `apis.kcp.io:binding:a-team`.
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

// ResourceSchema defines the resource schemas that are exposed with this APIExport.
//
// +kubebuilder:validation:XValidation:rule="has(self.schema) || (has(self.storage) && has(self.storage.adopted))",message="schema is required unless storage.adopted is set"
type ResourceSchema struct {
	// Name is the name of the resource.
	Name string `json:"name"`
	// Group is the API group of the resource. Empty string represents the core group.
	Group string `json:"group"`
	// Schema is the name of the referenced APIResourceSchema. This must be of the format
	// "<version>.<name>.<group>".
	//
	// It is optional for a resource with adopted storage: such a resource is
	// served with the schema the source export references for it, which kcp
	// resolves and records in status.resources. If set on an adopted resource it
	// must name the same APIResourceSchema the source uses.
	//
	// +optional
	Schema string `json:"schema,omitempty"`
	// Storage defines how the resource is stored.
	//
	// +kubebuilder:default={crd:{}}
	// +optional
	Storage ResourceSchemaStorage `json:"storage"`

	// claimableBy grants other APIExports the right to claim this resource
	// without naming this export's identity in their permission claim: a claim
	// for this group/resource on a granted export resolves to this export, and
	// the claimed export's maximal permission policy is satisfied by the grant.
	// Consumers still accept the claim on their APIBinding unless the grant is
	// delegated. A grant is never required: any export may claim this resource
	// by identity or by export reference, subject to consumer acceptance and the
	// maximal permission policy.
	//
	// +optional
	// +listType=atomic
	ClaimableBy []ClaimGrant `json:"claimableBy,omitempty"`
}

// ClaimGrant names exports that may claim a resource. Exactly one of export,
// exportSelector or rbac is set (enforced by a oneOf in the CRD schema patch
// and by admission).
type ClaimGrant struct {
	// export names one APIExport that may claim the resource.
	//
	// +optional
	Export *ExportBindingReference `json:"export,omitempty"`

	// exportSelector grants every APIExport whose labels match the selector.
	//
	// +optional
	ExportSelector *metav1.LabelSelector `json:"exportSelector,omitempty"`

	// rbac grants every APIExport whose owner is allowed the verb "claim" on
	// this APIExport, evaluated through RBAC in this export's workspace with
	// the same "apis.kcp.io:binding:" prefixing the maximal permission policy
	// uses.
	//
	// +optional
	RBAC *ClaimGrantRBAC `json:"rbac,omitempty"`

	// verbs caps the verbs a granted claim may use. Empty means no cap beyond
	// the claim's own verbs.
	//
	// +optional
	// +listType=set
	Verbs []string `json:"verbs,omitempty"`

	// delegated lets this export extend its own access to the grantee:
	// consumers bound to both exports are treated as having accepted the
	// grantee's claim, because this export already had that access and is
	// answerable for it. Default false: each consumer accepts the claim.
	//
	// +optional
	Delegated bool `json:"delegated,omitempty"`
}

// ClaimGrantRBAC grants claims through RBAC in the exporting workspace.
type ClaimGrantRBAC struct{}

// ResourceSchemaStorage defines how the resource is stored. Exactly one of
// crd, virtual or adopted is set (enforced by a oneOf in the CRD schema patch
// and by admission).
type ResourceSchemaStorage struct {
	// CRD storage defines that this APIResourceSchema is exposed as
	// CustomResourceDefinitions inside the workspaces that bind to the APIExport.
	// Like in vanilla Kubernetes, users can then create, update and delete
	// custom resources.
	//
	// +optional
	CRD *ResourceSchemaStorageCRD `json:"crd,omitempty"`

	// Virtual storage defines that this APIResourceSchema is exposed as
	// a projection of the referenced resource inside the workspaces that
	// bind to the APIExport.
	//
	// +optional
	Virtual *ResourceSchemaStorageVirtual `json:"virtual,omitempty"`

	// Adopted storage defines that this resource is exposed as
	// CustomResourceDefinitions like crd storage, but stored on another
	// export's storage prefix: bindings of this export start on the prefix
	// the source export's bindings use for the same resource, so the objects
	// the source's consumers created are served in place. This is how one
	// export takes over serving a resource from another (zero-copy adoption).
	// The source must serve the same resource with crd storage and list this
	// export in its crd.sharedWith. The resource is served with the source's
	// schema.
	//
	// +optional
	Adopted *ResourceSchemaStorageAdopted `json:"adopted,omitempty"`
}

// ResourceSchemaStorageCRD configures crd storage.
type ResourceSchemaStorageCRD struct {
	// sharedWith lists exports whose bindings for this resource may sit on
	// this export's storage prefix (adopted storage on their side). This is
	// the owner's half of a storage handover; it is not how other exports
	// get access to the resource, that is a permission claim. Edited only by
	// this export's owner.
	//
	// +optional
	// +listType=atomic
	SharedWith []ExportBindingReference `json:"sharedWith,omitempty"`
}

// ResourceSchemaStorageAdopted configures adopted storage.
type ResourceSchemaStorageAdopted struct {
	// from is the export whose storage prefix for this resource is adopted.
	//
	// +required
	From ExportBindingReference `json:"from"`
}

// ResourceSchemaStorageVirtual refers to an endpoint slice object
// from which the virtual resource is served.
type ResourceSchemaStorageVirtual struct {
	// Reference points to another object that has a URL to a virtual workspace
	// in a "url" field in its status. The object can be of any kind.
	Reference corev1.TypedLocalObjectReference `json:"reference"`

	// Resource selector TBD.
	// We are not sure if it belongs here.
}

// Identity configures the identity of an APIExport: an opaque, unique,
// immutable identifier that is also the default etcd prefix of the export's
// bound instances. It is assigned by kcp and recorded in status.identityHash.
type Identity struct {
	// secretRef is a reference to a secret that contains the API identity in the 'key' file.
	//
	// Deprecated: identities carry no secret. The field is honored once, for
	// an export that has no identity yet, so flows that pre-create a secret to
	// pin an identity keep landing on it; it is never created by kcp when the
	// APIIdentityDecoupling feature gate is enabled, and it will be removed.
	// Use value to pin an identity.
	//
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// value pins the identity of this export instead of letting kcp generate
	// one. It is honored only while status.identityHash is empty. The value
	// must be prefixed with the export's own logical cluster name followed by
	// "-", so that ownership is checkable without a lookup, and must not be
	// held by another export. Used to keep an identity across a
	// delete-and-recreate of the export, e.g. by GitOps.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-z0-9][a-z0-9.-]*)?$`
	Value string `json:"value,omitempty"`
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
// with "apis.kcp.io:binding:".
type LocalAPIExportPolicy struct{}

// PermissionClaim identifies an object by GR and identity hash.
// Its purpose is to determine the added permissions that a service provider may
// request and that a consumer may accept and allow the service provider access to.
//
// +kubebuilder:validation:XValidation:rule="!self.resource.contains('/') || !has(self.defaultSelector)",message="defaultSelector is not allowed on subresource claims"
type PermissionClaim struct {
	GroupResource `json:",inline"`

	// verbs is a list of supported API operation types (this includes
	// but is not limited to get, list, watch, create, update, patch,
	// delete, deletecollection, and proxy).
	//
	// +required
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	Verbs []string `json:"verbs"`

	// This is the identity for a given APIExport that the APIResourceSchema belongs to.
	// The hash can be found on APIExport and APIResourceSchema's status.
	// It will be empty for core types.
	// Note that one must look this up for a particular kcp instance.
	//
	// It may be left empty for a resource of another export when export names
	// that export, or when the claimed export grants this export the claim
	// through claimableBy; kcp then resolves the identity and records it in
	// status.permissionClaims.
	//
	// +kubebuilder:default:=""
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// export names the APIExport whose resource is claimed, as an alternative
	// to identityHash. kcp resolves it to the export's identity. At most one
	// of export and identityHash is set.
	//
	// +optional
	Export *ExportBindingReference `json:"export,omitempty"`

	// defaultSelector is the default selector to use when creating APIBindings
	// via WorkspaceType's defaultAPIBindings. If not set, the APIBinding will
	// default to matchAll: true.
	//
	// This allows API providers to suggest a default scope for permission claims
	// that will be used when workspaces are automatically created with default
	// APIBindings. Users can always override the default selector by accepting the
	// claim with a different selector or by manually creating APIBindings with a custom selector.
	//
	// +optional
	DefaultSelector *PermissionClaimSelector `json:"defaultSelector,omitempty"`
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

// EqualGRI returns whether claim is equal in Group, Resource and Identity Hash.
func (p PermissionClaim) EqualGRI(claim PermissionClaim) bool {
	return p.Group == claim.Group &&
		p.Resource == claim.Resource &&
		p.IdentityHash == claim.IdentityHash
}

var _ apis.GroupResource = &GroupResource{}

// GroupResource identifies a resource.
type GroupResource struct {
	// group is the name of an API group.
	// For core groups this is the empty string '""'.
	//
	// +kubebuilder:validation:Pattern=`^(|[a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?)$`
	// +kubebuilder:default:=""
	// +optional
	Group string `json:"group,omitempty"`

	// resource is the name of the resource.
	// A subresource may be claimed as "resource/subresource",
	// e.g. "serviceaccounts/token", in the style of RBAC rules.
	// Note: it is worth noting that you can not ask for permissions for resource provided by a CRD
	// not provided by an api export.
	//
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9](/[a-z][-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=127
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
}

func (p GroupResource) GetGroup() string {
	return p.Group
}

func (p GroupResource) GetResource() string {
	return p.Resource
}

// APIExportStatus defines the observed state of APIExport.
type APIExportStatus struct {
	// identityHash is the identity of this APIExport: an opaque, unique
	// identifier that is also the default etcd storage prefix of its bound
	// instances. It is assigned once by kcp and immutable afterwards.
	//
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// identityAliasHashes lists previous identity hashes of this APIExport
	// that are still honored as equivalents of the current identityHash for
	// the resources this export serves. Permission claims and wildcard
	// consumers referencing an alias hash keep resolving to this export until
	// the alias is retired. Set by the one-time conversion of an identity that
	// was shared by several exports; managed by kcp.
	//
	// +optional
	// +listType=set
	IdentityAliasHashes []string `json:"identityAliasHashes,omitempty"`

	// identity describes where the identity came from.
	//
	// +optional
	Identity *IdentityStatus `json:"identity,omitempty"`

	// storage reports, per resource of this export, the storage prefixes its
	// bindings are expected to use: the export's own identity in the steady
	// state, the source's identity for adopted resources.
	//
	// +optional
	Storage *StorageStatus `json:"storage,omitempty"`

	// resources reports, per resource of this export, the schema in effect.
	// It differs from spec only for adopted resources, which are served with
	// the source export's schema.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	Resources []ResourceStatus `json:"resources,omitempty"`

	// permissionClaims records how every permission claim of spec that does
	// not name an identity resolved: to which identity and by which means.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	PermissionClaims []ResolvedPermissionClaim `json:"permissionClaims,omitempty"`

	// conditions is a list of conditions that apply to the APIExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// virtualWorkspaces contains all APIExport virtual workspace URLs.
	// this field is empty unless kcp has been started with the
	// 'EnableDeprecatedAPIExportVirtualWorkspacesUrls' feature gate.
	//
	// Deprecated: use APIExportEndpointSlice.status.endpoints instead. This
	// field will be removed in an upcoming API version.
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

// IdentitySource says where an export's identity came from.
type IdentitySource string

const (
	// IdentitySourceGenerated marks an identity generated by kcp.
	IdentitySourceGenerated IdentitySource = "Generated"
	// IdentitySourcePinned marks an identity pinned through spec.identity.value.
	IdentitySourcePinned IdentitySource = "Pinned"
	// IdentitySourceLegacy marks an identity derived from an identity secret.
	IdentitySourceLegacy IdentitySource = "Legacy"
	// IdentitySourceWellKnown marks the well-known identity of a core export.
	IdentitySourceWellKnown IdentitySource = "WellKnown"
	// IdentitySourceConverted marks an identity assigned by the one-time
	// conversion of an identity that was shared by several exports.
	IdentitySourceConverted IdentitySource = "Converted"
)

// IdentityStatus describes an export's identity.
type IdentityStatus struct {
	// source says where the identity came from.
	//
	// +optional
	// +kubebuilder:validation:Enum=Generated;Pinned;Legacy;WellKnown;Converted
	Source IdentitySource `json:"source,omitempty"`
}

// StorageStatus reports the storage prefixes of an export's resources.
type StorageStatus struct {
	// resources lists, per resource, the storage prefixes its bindings use.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	Resources []ResourceStorageStatus `json:"resources,omitempty"`
}

// ResourceStorageStatus reports the storage prefixes of one resource.
type ResourceStorageStatus struct {
	GroupResource `json:",inline"`

	// keys lists the storage prefixes bindings of this resource use: the
	// export's identity, or the source's identity for an adopted resource.
	//
	// +optional
	// +listType=set
	Keys []string `json:"keys,omitempty"`
}

// ResourceStatus reports the schema in effect for one resource.
type ResourceStatus struct {
	GroupResource `json:",inline"`

	// schema is the name of the APIResourceSchema in effect.
	//
	// +optional
	Schema string `json:"schema,omitempty"`

	// schemaCluster is the logical cluster the schema lives in: this export's
	// cluster, or the source export's cluster for an adopted resource.
	//
	// +optional
	SchemaCluster string `json:"schemaCluster,omitempty"`
}

// ClaimResolution says how a permission claim resolved to an identity.
type ClaimResolution string

const (
	// ClaimResolvedByIdentityHash: the claim named the identity itself.
	ClaimResolvedByIdentityHash ClaimResolution = "IdentityHash"
	// ClaimResolvedByExport: the claim named the export.
	ClaimResolvedByExport ClaimResolution = "Export"
	// ClaimResolvedByGrant: the claimed export grants this export the claim.
	ClaimResolvedByGrant ClaimResolution = "Grant"
	// ClaimResolvedBuiltIn: the claim is for a built-in or apis.kcp.io
	// resource and has no identity.
	ClaimResolvedBuiltIn ClaimResolution = "BuiltIn"
)

// ResolvedPermissionClaim records the resolution of one permission claim.
type ResolvedPermissionClaim struct {
	GroupResource `json:",inline"`

	// identityHash is the resolved identity of the claimed export.
	//
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// resolvedBy says how the identity was resolved.
	//
	// +optional
	// +kubebuilder:validation:Enum=IdentityHash;Export;Grant;BuiltIn
	ResolvedBy ClaimResolution `json:"resolvedBy,omitempty"`

	// delegated is true when the resolving grant delegates the claimed
	// export's access: consumers bound to both exports need not accept.
	//
	// +optional
	Delegated bool `json:"delegated,omitempty"`

	// verbs is the effective verb set of the claim: the claim's verbs capped
	// by the resolving grant's verbs, if the grant caps them. Empty means the
	// claim's own verbs apply.
	//
	// +optional
	// +listType=set
	Verbs []string `json:"verbs,omitempty"`
}

// ResolvedPermissionClaims returns the export's permission claims with the
// identity hash filled in from status.permissionClaims where the spec left it
// empty and kcp resolved it. Everything that matches claims against an
// export's claims must use this instead of spec.permissionClaims.
func (in *APIExport) ResolvedPermissionClaims() []PermissionClaim {
	if in == nil {
		return nil
	}
	claims := make([]PermissionClaim, len(in.Spec.PermissionClaims))
	copy(claims, in.Spec.PermissionClaims)
	if len(in.Status.PermissionClaims) == 0 {
		return claims
	}
	for i := range claims {
		if claims[i].IdentityHash != "" {
			continue
		}
		for _, resolved := range in.Status.PermissionClaims {
			if resolved.Group == claims[i].Group && resolved.Resource == claims[i].Resource {
				claims[i].IdentityHash = resolved.IdentityHash
				if len(resolved.Verbs) > 0 {
					claims[i].Verbs = append([]string(nil), resolved.Verbs...)
				}
				break
			}
		}
	}
	return claims
}

// DelegatedClaim reports whether the export's claim for the group/resource
// resolved through a delegated grant.
func (in *APIExport) DelegatedClaim(group, resource string) bool {
	if in == nil {
		return false
	}
	for _, resolved := range in.Status.PermissionClaims {
		if resolved.Group == group && resolved.Resource == resource {
			return resolved.Delegated
		}
	}
	return false
}

// MatchClaim finds the export claim an accepted claim refers to: an exact
// group/resource/identity match, or, when the accepted claim names no
// identity, the single export claim for that group/resource. The returned
// claim carries the resolved identity.
func MatchClaim(accepted PermissionClaim, exportClaims []PermissionClaim) (PermissionClaim, bool) {
	for _, c := range exportClaims {
		if c.EqualGRI(accepted) {
			return c, true
		}
	}
	if accepted.IdentityHash != "" {
		return PermissionClaim{}, false
	}
	var match PermissionClaim
	found := 0
	for _, c := range exportClaims {
		if c.Group == accepted.Group && c.Resource == accepted.Resource {
			match = c
			found++
		}
	}
	if found == 1 {
		return match, true
	}
	return PermissionClaim{}, false
}

// APIExportList is a list of APIExport resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExport `json:"items"`
}
