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
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

const (
	// InternalAPIBindingExportLabelKey is the label key on an APIBinding with the
	// base62(sha224(<clusterName>:<exportName>)) as value to filter bindings by export.
	InternalAPIBindingExportLabelKey = "internal.apis.kcp.dev/export"
)

// APIBinding enables a set of resources and their behaviour through an external
// service provider in this workspace.
//
// The service provider uses an APIExport to expose the API.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIBinding struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +required
	// +kubebuilder:validation:Required
	Spec APIBindingSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status APIBindingStatus `json:"status,omitempty"`
}

func (in *APIBinding) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIBinding) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// APIBindingSpec records the APIs and implementations that are to be bound.
type APIBindingSpec struct {
	// reference uniquely identifies an API to bind to.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="APIExport reference must not be changed"
	Reference BindingReference `json:"reference"`

	// permissionClaims records decisions about permission claims requested by the API service provider.
	// Individual claims can be accepted or rejected. If accepted, the API service provider gets the
	// requested access to the specified resources in this workspace. Access is granted per
	// GroupResource, identity, and other properties.
	//
	// +optional
	PermissionClaims []AcceptablePermissionClaim `json:"permissionClaims,omitempty"`
}

// AcceptablePermissionClaim is a PermissionClaim that records if the user accepts or rejects it.
type AcceptablePermissionClaim struct {
	PermissionClaim `json:",inline"`

	// state indicates if the claim is accepted or rejected.

	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Accepted;Rejected
	State AcceptablePermissionClaimState `json:"state"`
}

type AcceptablePermissionClaimState string

const (
	ClaimAccepted AcceptablePermissionClaimState = "Accepted"
	ClaimRejected AcceptablePermissionClaimState = "Rejected"
)

// BindingReference describes a reference to an APIExport. Exactly one of the
// fields must be set.
type BindingReference struct {
	// export is a reference to an APIExport by cluster name and export name.
	// The creator of the APIBinding needs to have access to the APIExport with the
	// verb `bind` in order to bind to it.
	//
	// +optional
	Export *ExportBindingReference `json:"export,omitempty"`
}

// ExportBindingReference is a reference to an APIExport by cluster and name.
type ExportBindingReference struct {
	// cluster is a logical cluster name where the APIExport is defined.
	// If identifier and path are unset, the cluster of the APIBinding is used.
	//
	// +optional
	Cluster logicalcluster.Name `json:"cluster,omitempty"`

	// name is the name of the APIExport that describes the API.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	Name string `json:"name"`
}

// APIBindingPhaseType is the type of the current phase of an APIBinding.
type APIBindingPhaseType string

const (
	APIBindingPhaseBinding APIBindingPhaseType = "Binding"
	APIBindingPhaseBound   APIBindingPhaseType = "Bound"
)

// APIBindingStatus records which schemas are bound.
type APIBindingStatus struct {
	// boundResources records the state of bound APIs.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	BoundResources []BoundAPIResource `json:"boundResources,omitempty"`

	// phase is the current phase of the APIBinding:
	// - "": the APIBinding has just been created, waiting to be bound.
	// - Binding: the APIBinding is being bound.
	// - Bound: the APIBinding is bound and the referenced APIs are available in the workspace.
	//
	// +optional
	// +kubebuilder:validation:Enum="";Binding;Bound
	Phase APIBindingPhaseType `json:"phase,omitempty"`

	// conditions is a list of conditions that apply to the APIBinding.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// appliedPermissionClaims is a list of the permission claims the system has seen and applied,
	// according to the requests of the API service provider in the APIExport and the acceptance
	// state in spec.permissionClaims.
	//
	// +optional
	AppliedPermissionClaims []PermissionClaim `json:"appliedPermissionClaims,omitempty"`

	// exportPermissionClaims records the permissions that the export provider is asking for
	// the binding to grant.
	// +optional
	ExportPermissionClaims []PermissionClaim `json:"exportPermissionClaims,omitempty"`
}

// These are valid conditions of APIBinding.
const (
	// APIExportValid is a condition for APIBinding that reflects the validity of the referenced APIExport.
	APIExportValid conditionsv1alpha1.ConditionType = "APIExportValid"

	// APIExportInvalidReferenceReason is a reason for the APIExportValid condition of APIBinding that the referenced
	// APIExport reference is invalid.
	APIExportInvalidReferenceReason = "APIExportInvalidReference"
	// APIExportNotFoundReason is a reason for the APIExportValid condition that the referenced APIExport is not found.
	APIExportNotFoundReason = "APIExportNotFound"

	// APIResourceSchemaInvalidReason is a reason for the InitialBindingCompleted and BindingUpToDate conditions when one of generated CRD is invalid.
	APIResourceSchemaInvalidReason = "APIResourceSchemaInvalid"

	// InternalErrorReason is a reason used by multiple conditions that something went wrong.
	InternalErrorReason = "InternalError"

	// InitialBindingCompleted is a condition for APIBinding that indicates the initial binding completed successfully.
	// Once true, this can never be reset to false.
	InitialBindingCompleted conditionsv1alpha1.ConditionType = "InitialBindingCompleted"

	// WaitingForEstablishedReason is a reason for the InitialBindingCompleted condition that the bound CRDs are not ready.
	WaitingForEstablishedReason = "WaitingForEstablished"

	// BindingUpToDate is a condition for APIBinding that indicates that the APIs currently bound are up-to-date with
	// the binding's desired export.
	BindingUpToDate conditionsv1alpha1.ConditionType = "BindingUpToDate"

	// NamingConflictsReason is a reason for the BindingUpToDate condition that at least one API coming in from the APIBinding
	// has a naming conflict with other APIs.
	NamingConflictsReason = "NamingConflicts"

	// BindingResourceDeleteSuccess is a condition for APIBinding that indicates the resources relating this binding are deleted
	// successfully when the APIBinding is deleting
	BindingResourceDeleteSuccess conditionsv1alpha1.ConditionType = "BindingResourceDeleteSuccess"

	// PermissionClaimsValid is a condition for APIBinding that indicates that the permission claims were valid or not.
	PermissionClaimsValid conditionsv1alpha1.ConditionType = "PermissionClaimsValid"

	// InvalidPermissionClaimsReason indicates there were unexpected and/or invalid permission claims (e.g. due to
	// identity mismatch).
	InvalidPermissionClaimsReason = "InvalidPermissionClaims"

	// PermissionClaimsApplied is a condition for APIBinding that indicates that all the accepted permission claims
	// have been applied.
	PermissionClaimsApplied conditionsv1alpha1.ConditionType = "PermissionClaimsApplied"
)

// These are annotations for bound CRDs
const (
	// AnnotationBoundCRDKey is the annotation key that indicates a CRD is for an APIExport (a "bound CRD").
	AnnotationBoundCRDKey = "apis.kcp.dev/bound-crd"
	// AnnotationSchemaClusterKey is the annotation key for a bound CRD indicating the cluster name of the
	// APIResourceSchema for the CRD.
	AnnotationSchemaClusterKey = "apis.kcp.dev/schema-cluster"
	// AnnotationSchemaNameKey is the annotation key for a bound CRD indicating the name of the APIResourceSchema for
	// the CRD.
	AnnotationSchemaNameKey = "apis.kcp.dev/schema-name"
	// AnnotationAPIIdentityKey is the annotation key for a bound CRD indicating the identity hash of the APIExport
	// for the request. This data is synthetic; it is not stored in etcd and instead is only applied when retrieving
	// CRs for the CRD.
	AnnotationAPIIdentityKey = "apis.kcp.dev/identity"
)

// BoundAPIResource describes a bound GroupVersionResource through an APIResourceSchema of an APIExport..
type BoundAPIResource struct {
	// group is the group of the bound API. Empty string for the core API group.
	//
	// +required
	Group string `json:"group"`

	// resource is the resource of the bound API.
	//
	// kubebuilder:validation:MinLength=1
	// +required
	Resource string `json:"resource"`

	// Schema references the APIResourceSchema that is bound to this API.
	//
	// +required
	Schema BoundAPIResourceSchema `json:"schema"`

	// storageVersions lists all versions of a resource that were ever persisted. Tracking these
	// versions allows a migration path for stored versions in etcd. The field is mutable
	// so a migration controller can finish a migration to another version (ensuring
	// no old objects are left in storage), and then remove the rest of the
	// versions from this list.
	//
	// Versions may not be removed while they exist in this list.
	//
	// +optional
	// +listType=set
	StorageVersions []string `json:"storageVersions,omitempty"`
}

// BoundAPIResourceSchema is a reference to an APIResourceSchema.
type BoundAPIResourceSchema struct {
	// name is the bound APIResourceSchema name.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// UID is the UID of the APIResourceSchema that is bound to this API.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	UID string `json:"UID"`

	// identityHash is the hash of the API identity that this schema is bound to.
	// The API identity determines the etcd prefix used to persist the object.
	// Different identity means that the objects are effectively served and stored
	// under a distinct resource. A CRD of the same GroupVersionResource uses a
	// different identity and hence a separate etcd prefix.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	IdentityHash string `json:"identityHash"`
}

// APIBindingList is a list of APIBinding resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIBinding `json:"items"`
}
