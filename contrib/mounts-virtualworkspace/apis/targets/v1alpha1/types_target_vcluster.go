/*
Copyright 2023 The KCP Authors.

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
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TargetVCluster describes the current VCluster target object.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=contrib
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="ClusterReady")].status`
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.phase`
type TargetVCluster struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec TargetVClusterSpec `json:"spec,omitempty"`
	// +optional
	Status TargetVClusterStatus `json:"status,omitempty"`
}

// TargetVClusterSpec is the specification of the Target Kube cluster proxy resource.
type TargetVClusterSpec struct {
	// SecretRef is a reference to the secret containing the kubeconfig for the target cluster.
	SecretRef corev1.ObjectReference `json:"secretRef,omitempty"`
}

// TargetVClusterStatus communicates the observed state of the Kube cluster proxy.
type TargetVClusterStatus struct {
	// Phase of the cluster proxy (Initializing, Ready).
	//
	// +kubebuilder:default=Initializing
	Phase tenancyv1alpha1.MountPhaseType `json:"phase,omitempty"`

	// A timestamp indicating when the proxy last reported status.
	// +optional
	LastProxyHeartbeatTime *v1.Time `json:"lastProxyHeartbeatTime,omitempty"`

	// Current processing state of the Cluster proxy.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// SecretString is mountpoint secret string for clients to mount.
	SecretString string `json:"secretString,omitempty"`

	// URL is the address under which mount should be using.
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`
}

// TargetVClusterList is a list of TargetVCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type TargetVClusterList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []TargetVCluster `json:"items"`
}

func (in *TargetVCluster) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *TargetVCluster) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}
