/*
Copyright 2021 The Authors

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
)

// Cluster describes a member cluster.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

type Cluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

// ClusterSpec holds the desired state of the Cluster (from the client).
type ClusterSpec struct {
	KubeConfig string `json:"kubeconfig"`
}

// ClusterStatus communicates the observed state of the Cluster (from the controller).
type ClusterStatus struct {
	Conditions Conditions `json:"conditions,omitempty"`
}

func (cs *ClusterStatus) SetConditionReady(status corev1.ConditionStatus, reason, message string) {
	for idx, cond := range cs.Conditions {
		if cond.Type == ClusterConditionReady {
			cs.Conditions[idx] = Condition{
				Type:               ClusterConditionReady,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: cond.LastTransitionTime,
			}
			return
		}
	}
	cs.Conditions = append(cs.Conditions, Condition{
		Type:               ClusterConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

// ClusterList is a list of Cluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Cluster `json:"items"`
}
