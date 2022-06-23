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

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// APISet records a set of API Bindings that need to be created
// and bound before initialization is complete. This configuration
// applies to all workspaces of the type that shares a name with this
// object.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
type APISet struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec APISetSpec `json:"spec,omitempty"`

	// +optional
	Status APISetStatus `json:"status,omitempty"`
}

// APISetSpec holds the desired state of the APISet.
type APISetSpec struct {
	// bindings are the API Bindings to create during initialization.
	// +optional
	Bindings []apisv1alpha1.APIBindingSpec `json:"bindings,omitempty"`
}

// APISetStatus communicates the observed state of the APISet.
type APISetStatus struct {
	// conditions is a list of conditions that apply to the APISet.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *APISet) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APISet) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid condiitons of APISet
const (
	// APIBindingsValid is a condition for APISet that reflects the validity of the referenced APISet and.
	// the APIExports they correspond to. Workspaces cannot be initialized if this condition is not met.
	APIBindingsValid conditionsv1alpha1.ConditionType = "APIBindingsValid"

	// APIBindingInvalidReason is a reason for the APIBindingsValid condition of APISet that one or more
	// of the APIBindings in the APISet is invalid.
	APIBindingInvalidReason = "APIBindingInvalid"
)

// APISetList is a list of APISet resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APISetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APISet `json:"items"`
}
