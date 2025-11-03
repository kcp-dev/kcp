/*
Copyright 2024 The KCP Authors.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

const CachedResourceFinalizer = "cachedresource.cache.kcp.dev"

const (
	// CachedResourceEndpointSliceSkipAnnotation is an annotation that can be set on a CachedResource to skip the creation of default CachedResourceEndpointSlice.
	CachedResourceEndpointSliceSkipAnnotation = "cachedresources.cache.kcp.io/skip-endpointslice"
)

// CachedResource defines a resource that should be published to other workspaces
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=`.spec.resource`,description="Resource type being published"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CachedResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CachedResourceSpec   `json:"spec"`
	Status CachedResourceStatus `json:"status,omitempty"`
}

// CachedResourceSpec defines the desired state of CachedResource.
type CachedResourceSpec struct {
	// GroupVersionResource is the fully qualified name of the resource to be published.
	GroupVersionResource `json:",inline"`

	// identity points to a secret that contains the API identity in the 'key' file.
	// The API identity allows access to CachedResource's resources via the APIExport.
	//
	// Different  CachedResource in a workspace can share a common identity, or have different
	// ones. The identity (the secret) can also be transferred to another workspace
	// when the  ublishedResource is moved.
	//
	// The identity is defaulted. A secret with the name of the CachedResource is automatically
	// created.
	//
	// +optional
	Identity *Identity `json:"identity,omitempty"`

	// LabelSelector is used to filter which resources should be published
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// Identity defines the identity of an CachedResource, i.e. determines the cached resource access
// of the resources, that are published by this CachedResource.
type Identity struct {
	// secretRef is a reference to a secret that contains the API identity in the 'key' file.
	//
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// GroupVersionResource identifies a resource.
type GroupVersionResource struct {
	// group is the name of an API group.
	// For core groups this is the empty string '""'.
	//
	// +kubebuilder:validation:Pattern=`^(|[a-z0-9]([-a-z0-9]*[a-z0-9](\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*)?)$`
	// +optional
	Group string `json:"group,omitempty"`

	// version is the version of the resource.
	// +optional
	Version string `json:"version,omitempty"`

	// resource is the name of the resource.
	// Note: it is worth noting that you can not ask for permissions for resource provided by a CRD
	// not provided by an api export.
	// +kubebuilder:validation:Pattern=`^[a-z][-a-z0-9]*[a-z0-9]$`
	// +required
	// +kubebuilder:validation:Required
	Resource string `json:"resource"`
}

// CachedResourcePhaseType is the type of the current phase of the published resource.
//
// +kubebuilder:validation:Enum=Scheduling;Initializing;Ready;Deleting;Deleted
type CachedResourcePhaseType string

const (
	CachedResourcePhaseInitializing CachedResourcePhaseType = "Initializing"
	CachedResourcePhaseReady        CachedResourcePhaseType = "Ready"
	CachedResourcePhaseDeleting     CachedResourcePhaseType = "Deleting"
	CachedResourcePhaseDeleted      CachedResourcePhaseType = "Deleted"
)

// These are valid conditions of published resource.
const (
	// CachedResourceValid represents status of the scheduling process for this published resource.
	CachedResourceValid conditionsv1alpha1.ConditionType = "ResourceValid"

	// ReplicationStarted represents status of the replication process for this published resource.
	ReplicationStarted conditionsv1alpha1.ConditionType = "ReplicationStarted"
)

const (
	// CachedResourceIdentityValid represents status of the identity generation process for this published resource.
	CachedResourceIdentityValid conditionsv1alpha1.ConditionType = "IdentityValid"

	IdentityGenerationFailedReason   = "IdentityGenerationFailed"
	IdentityVerificationFailedReason = "IdentityVerificationFailed"
)

const (
	// CachedResourceInvalidReferenceReason is a reason for the CachedResourceValid condition that the referenced
	// CachedResource reference is invalid.
	CachedResourceInvalidReferenceReason = "CachedResourceInvalidReference"
	// CachedResourceNotFoundReason is a reason for the CachedResourceValid condition that the referenced CachedResource is not found.
	CachedResourceNotFoundReason = "CachedResourceNotFound"

	// InternalErrorReason is a reason used by multiple conditions that something went wrong.
	InternalErrorReason = "InternalError"
)

// These are valid reasons of published resource.
const (
	CachedResourceValidNoResources   = "NoResources"
	CachedResourceValidDeleting      = "Deleting"
	CachedResourceReplicationStarted = "Started"
)

// CachedResourceStatus defines the observed state of CachedResource.
type CachedResourceStatus struct {
	// IdentityHash is a hash of the identity configuration
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// ResourceCount is the number of resources that match the label selector
	// +optional
	ResourceCounts *ResourceCount `json:"resourceCounts,omitempty"`

	// Phase of the workspace (Initializing, Ready, Unavailable).
	//
	// +kubebuilder:default=Initializing
	Phase CachedResourcePhaseType `json:"phase,omitempty"`

	// Current processing state of the Workspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ResourceCount is the number of resources that match the label selector
// and are cached in the cache.
type ResourceCount struct {
	Cache int `json:"cache"`
	Local int `json:"local"`
}

// CachedResourceReference is a reference to a CachedResource.
type CachedResourceReference struct {
	// name is the name of the CachedResource the reference points to.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	Name string `json:"name"`
}

// CachedResourceList contains a list of CachedResource
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CachedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CachedResource `json:"items"`
}

func (in *CachedResource) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *CachedResource) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in GroupVersionResource) GetGroup() string {
	return in.Group
}

func (in GroupVersionResource) GetResource() string {
	return in.Resource
}
