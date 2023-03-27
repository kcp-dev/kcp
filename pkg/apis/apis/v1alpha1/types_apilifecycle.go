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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// APILifecycle
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APILifecycle struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +required
	// +kubebuilder:validation:Required
	Spec APILifecycleSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status APILifecycleStatus `json:"status,omitempty"`
}

func (in *APILifecycle) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APILifecycle) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// APILifecycleSpec records the APIs and implementations that are to be bound.
type APILifecycleSpec struct {
	// reference uniquely identifies an APIExport
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="APIExport reference must not be changed"
	Reference string `json:"reference"`

	// hooks hold all possible lifecycle hooks
	//
	// +required
	// +kubebuilder:validation:Required
	Hooks APILifecycleHooks `json:"hooks"`

	// permissionClaims records decisions about permission claims requested by the API service provider.
	// Individual claims can be accepted or rejected. If accepted, the API service provider gets the
	// requested access to the specified resources in this workspace. Access is granted per
	// GroupResource, identity, and other properties.
	//
	// +optional
	PermissionClaims []PermissionClaim `json:"permissionClaims,omitempty"`
}

// APILifecycleHooks defines the lifecycle hooks
type APILifecycleHooks struct {
	// bind is invoked when a binding is created and updated
	// +optional
	Bind *APILifecycleHook `json:"bind"`
}

type APILifecycleHook struct {
	// url where the hook is located
	URL string `json:"url"`
}

// APIBindingStatus records which schemas are bound.
type APILifecycleStatus struct {

	// conditions is a list of conditions that apply to the APIBinding.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// These are valid conditions of APIBinding.
const (
	// APILifecycleValid is a condition for APIBinding that reflects the validity of the referenced APIExport.
	APILifecycleValid conditionsv1alpha1.ConditionType = "APIExportValid"
)

// APILifecyleList is a list of APIBinding resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APILifecycleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APILifecycle `json:"items"`
}
