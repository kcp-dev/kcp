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
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
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
	Reference ExportReference `json:"reference"`
}

// ExportReference describes a reference to an APIExport. Exactly one of the
// fields must be set.
type ExportReference struct {
	// workspace is a reference to an APIExport in the same organization. The creator
	// of the APIBinding needs to have access to the APIExport with the verb `bind`
	// in order to bind to it.
	//
	// +optional
	Workspace *WorkspaceExportReference `json:"workspace,omitempty"`
}

// WorkspaceExportReference describes an API and backing implementation that are provided by an actor in the
// specified Workspace.
type WorkspaceExportReference struct {
	// path is an absolute reference to a workspace, e.g. root:org:ws. The workspace must
	// be some ancestor or a child of some ancestor.
	// +optional
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path,omitempty"`

	// Name of the APIExport that describes the API.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	ExportName string `json:"exportName"`
}

// APIBindingPhaseType is the type of the current phase of an APIBinding.
type APIBindingPhaseType string

const (
	APIBindingPhaseBinding APIBindingPhaseType = "Binding"
	APIBindingPhaseBound   APIBindingPhaseType = "Bound"
)

// APIBindingStatus records which schemas are bound.
type APIBindingStatus struct {
	// boundExport records the export this binding is bound to currently. It can
	// differ from the export that was specified in the spec while rebinding
	// to a different APIExport.
	//
	// This field is what gives the APIExport visibility into the objects in this
	// workspace.
	//
	// +optional
	BoundAPIExport *ExportReference `json:"boundExport,omitempty"`

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
// APIExports cannot be deleted until status.resourceSchemasInUse is empty.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
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
	Identity *Identity `json:"identity"`

	// TODO: before beta we should re-evaluate this field name

	// maximalPermissionPolicy will allow for a service provider to set a upper bound on what is allowed
	// for a consumer of this API. If the policy is not set, no upper bound is applied,
	// i.e the consuming users can do whatever the user workspace allows the user to do.
	// +optional
	MaximalPermissionPolicy *APIExportPolicy `json:"maximalPermissionPolicy,omitempty"`
}

// Identity defines the identity of an APIExport, i.e. determines the etcd prefix
// data of this APIExport are stored under.
type Identity struct {
	// secretRef is a reference to a secret that contains the API identity in the 'key' file.
	//
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef"`
}

// APIExportPolicy is a wrapper type around the multiple options that would be allowed.
type APIExportPolicy struct {
	// local is policy that is defined in same namespace as API Export.

	// +optional
	Local *LocalAPIExportPolicy `json:"local,omitempty"`
}

// LocalAPIExportPolicy will tell the APIBinding authorizer to check policy in the local namespace
// of the API Export
type LocalAPIExportPolicy struct{}

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

// APIResourceSchema describes a resource, identified by (group, version, resource, schema).
//
// A APIResourceSchema is immutable and cannot be deleted if they are referenced by
// an APIExport in the same workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type APIResourceSchema struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	//
	// +optional
	Spec APIResourceSchemaSpec `json:"spec,omitempty"`
}

// APIResourceSchemaSpec defines the desired state of APIResourceSchema.
type APIResourceSchemaSpec struct {
	// group is the API group of the defined custom resource. Empty string means the
	// core API group. 	The resources are served under `/apis/<group>/...` or `/api` for the core group.
	//
	// +required
	Group string `json:"group"`

	// names specify the resource and kind names for the custom resource.
	//
	// +required
	Names apiextensionsv1.CustomResourceDefinitionNames `json:"names"`
	// scope indicates whether the defined custom resource is cluster- or namespace-scoped.
	// Allowed values are `Cluster` and `Namespaced`.
	//
	// +required
	// +kubebuilder:validation:Enum=Cluster;Namespaced
	Scope apiextensionsv1.ResourceScope `json:"scope"`

	// versions is the API version of the defined custom resource.
	//
	// Note: the OpenAPI v3 schemas must be equal for all versions until CEL
	//       version migration is supported.
	//
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	Versions []APIResourceVersion `json:"versions"`
}

// APIResourceVersion describes one API version of a resource.
type APIResourceVersion struct {
	// name is the version name, e.g. “v1”, “v2beta1”, etc.
	// The custom resources are served under this version at `/apis/<group>/<version>/...` if `served` is true.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^v[1-9][0-9]*([a-z]+[1-9][0-9]*)?$
	Name string `json:"name"`
	// served is a flag enabling/disabling this version from being served via REST APIs
	//
	// +required
	// +kubebuilder:default=true
	Served bool `json:"served"`
	// storage indicates this version should be used when persisting custom resources to storage.
	// There must be exactly one version with storage=true.
	//
	// +required
	Storage bool `json:"storage"`
	// deprecated indicates this version of the custom resource API is deprecated.
	// When set to true, API requests to this version receive a warning header in the server response.
	// Defaults to false.
	//
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`
	// deprecationWarning overrides the default warning returned to API clients.
	// May only be set when `deprecated` is true.
	// The default warning indicates this version is deprecated and recommends use
	// of the newest served version of equal or greater stability, if one exists.
	//
	// +optional
	DeprecationWarning *string `json:"deprecationWarning,omitempty"`
	// schema describes the structural schema used for validation, pruning, and defaulting
	// of this version of the custom resource.
	//
	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	// +structType=atomic
	Schema runtime.RawExtension `json:"schema"`
	// subresources specify what subresources this version of the defined custom resource have.
	//
	// +optional
	Subresources apiextensionsv1.CustomResourceSubresources `json:"subresources"`
	// additionalPrinterColumns specifies additional columns returned in Table output.
	// See https://kubernetes.io/docs/reference/using-api/api-concepts/#receiving-resources-as-tables for details.
	// If no columns are specified, a single column displaying the age of the custom resource is used.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	AdditionalPrinterColumns []apiextensionsv1.CustomResourceColumnDefinition `json:"additionalPrinterColumns,omitempty"`
}

// APIResourceSchemaList is a list of APIResourceSchema resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIResourceSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIResourceSchema `json:"items"`
}

func (v *APIResourceVersion) GetSchema() (*apiextensionsv1.JSONSchemaProps, error) {
	if v.Schema.Raw == nil {
		return nil, nil
	}
	var schema apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(v.Schema.Raw, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}

func (v *APIResourceVersion) SetSchema(schema *apiextensionsv1.JSONSchemaProps) error {
	if schema == nil {
		v.Schema.Raw = nil
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	v.Schema.Raw = raw
	return nil
}
