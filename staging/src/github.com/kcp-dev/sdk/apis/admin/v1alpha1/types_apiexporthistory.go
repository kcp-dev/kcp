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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// APIExportHistory records what an APIExport has served in the past, so that a schema
// cannot be swapped for one that changes a property of an already existing group
// resource. Today only the resource scope is recorded.
//
// Its name is the SHA256 of "<cluster>|<name>" of the APIExport it belongs to and it is
// maintained by kcp in the system:bound-crds logical cluster. It is not meant to be
// created or edited by users.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIExportHistory struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status APIExportHistoryStatus `json:"status,omitempty"`
}

// APIExportHistoryStatus communicates the observed state of APIExportHistory.
type APIExportHistoryStatus struct {
	// Resources lists every group resource ever served by the APIExport, together
	// with the properties it was first served with.
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	Resources []ResourceHistory `json:"resources,omitempty"`
}

// ResourceHistory records what a single group resource has been served with.
type ResourceHistory struct {
	// Group is the API group of the recorded resource. Empty string means the core group.
	// +required
	Group string `json:"group"`

	// Resource is the plural name of the recorded resource.
	// +required
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// Scope is the resource scope this group resource was first served with.
	// +required
	// +kubebuilder:validation:Enum=Cluster;Namespaced
	Scope apiextensionsv1.ResourceScope `json:"scope"`
}

// APIExportHistoryList is a list of APIExportHistory resources.
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportHistoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExportHistory `json:"items"`
}
