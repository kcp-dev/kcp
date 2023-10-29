/*
Copyright 2021 The KCP Authors.

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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// Cluster describes the current cluster proxy.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type Cluster struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`
	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

type ProxyType string

const (
	ProxyTypePassthrough ProxyType = "Passthrough"
)

// ClusterSpec is the specification of the Cluster proxy resource.
type ClusterSpec struct {
	Type ProxyType `json:"type,omitempty"`
}

// ClusterStatus communicates the observed state of the Cluster.
type ClusterStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the cluster with standard Kubernetes
	// client libraries and command line tools via proxy.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// Phase of the cluster proxy (Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase ClusterPhaseType `json:"phase,omitempty"`

	// A timestamp indicating when the proxy last reported status.
	// +optional
	LastProxyHeartbeatTime *v1.Time `json:"lastProxyHeartbeatTime,omitempty"`

	// Current processing state of the Cluster proxy.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ClusterPhaseType is the type of the current phase of the cluster proxy
//
// +kubebuilder:validation:Enum=Scheduling;Initializing;Ready
type ClusterPhaseType string

const (
	ClusterPhaseScheduling   ClusterPhaseType = "Scheduling"
	ClusterPhaseInitializing ClusterPhaseType = "Initializing"
	ClusterPhaseReady        ClusterPhaseType = "Ready"
)

// ClusterList is a list of Cluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []Cluster `json:"items"`
}
