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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PublishedResource defines a resource that should be published to other workspaces
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
type PublishedResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PublishedResourceSpec   `json:"spec"`
	Status PublishedResourceStatus `json:"status,omitempty"`
}

// PublishedResourceSpec defines the desired state of PublishedResource
type PublishedResourceSpec struct {
	// GroupResource is the fully qualified name of the resource type to be published
	GroupResource `json:",inline"`

	// LabelSelector is used to filter which resources should be published
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
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

// PublishedResourceStatus defines the observed state of PublishedResource
type PublishedResourceStatus struct {
	// IdentityHash is a hash of the identity configuration
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`
}

// PublishedResourceList contains a list of PublishedResource
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PublishedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PublishedResource `json:"items"`
}
