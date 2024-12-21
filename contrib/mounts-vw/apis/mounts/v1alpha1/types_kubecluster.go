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

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeCluster describes the current KubeCluster proxy object.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=contrib
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="KubeClusterReady")].status`
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=`.status.phase`
type KubeCluster struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec KubeClusterSpec `json:"spec,omitempty"`
	// +optional
	Status KubeClusterStatus `json:"status,omitempty"`
}

// KubeClusterSpec is the specification of the Kube cluster proxy resource.
type KubeClusterSpec struct {
	// SecretString is used to identify Target cluster in the backend for mount.
	SecretString string `json:"secretString,omitempty"`
}

// KubeClusterStatus communicates the observed state of the Kube cluster proxy.
type KubeClusterStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the cluster with standard Kubernetes
	// client libraries and command line tools via proxy.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

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
}

const (
	// KubeClusterReady represents readiness status of the KubeCluster proxy.
	KubeClusterReady conditionsv1alpha1.ConditionType = "KubeClusterReady"

	// KubeClusterAgentNotReadyReason is the reason for the KubeClusterReady condition
	KubeClusterAgentNotReadyReason = "AgentNotReady"
)

// KubeClusterList is a list of KubeCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type KubeClusterList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []KubeCluster `json:"items"`
}

func (in *KubeCluster) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *KubeCluster) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}
