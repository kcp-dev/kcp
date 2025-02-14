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

// VCluster describes the current VCluster proxy object.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=contrib
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="ClusterReady")].status`
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.phase`
type VCluster struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec VClusterSpec `json:"spec,omitempty"`
	// +optional
	Status VClusterStatus `json:"status,omitempty"`
}

// VClusterSpec is the specification of the VCluster cluster proxy resource.
type VClusterSpec struct {
	// Version is the version of the VCluster proxy.
	// +optional
	// +kubebuilder:default="0.20.2"
	Version string `json:"version,omitempty"`
	// Mode is the mode of the KubeCluster proxy(Direct, Delegated).
	// +kubebuilder:default=Delegated
	// +kubebuilder:validation:Enum=Delegated
	Mode KubeClusterMode `json:"mode,omitempty"`
	// SecretString is used to identify Target cluster in the backend for mount.
	// Used only in Delegated mode.
	// +optional
	SecretString *string `json:"secretString,omitempty"`
	// SecretRef is a reference to the secret containing the kubeconfig for the target cluster.
	// Used only in Direct mode.
	SecretRef *corev1.ObjectReference `json:"secretRef,omitempty"`
}

// VClusterMode is the mode of the VCluster proxy.
type VClusterMode string

// VClusterStatus communicates the observed state of the VCluster cluster proxy.
type VClusterStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the cluster with standard Kubernetes
	// client libraries and command line tools via proxy.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// SecretString is the secret string used to identify the target cluster in the backend for mount.
	// Used in both codes, as it is used to identify the target cluster in vcluster cluster pool.
	// +optional
	SecretString string `json:"secretString,omitempty"`

	// Phase of the cluster proxy (Initializing;Connecting;Ready;Unknown).
	//
	// +kubebuilder:default=Initializing
	Phase tenancyv1alpha1.MountPhaseType `json:"phase,omitempty"`

	// A timestamp indicating when the proxy last reported status.
	// +optional
	LastProxyHeartbeatTime *v1.Time `json:"lastProxyHeartbeatTime,omitempty"`

	// Current processing state of the Cluster proxy.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// VClusterList is a list of VCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type VClusterList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []VCluster `json:"items"`
}

func (in *VCluster) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *VCluster) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}
