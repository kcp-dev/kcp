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

// WorkspaceProxy describes the current WorkspaceProxy.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type WorkspaceProxy struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec WorkspaceProxySpec `json:"spec,omitempty"`
	// +optional
	Status WorkspaceProxyStatus `json:"status,omitempty"`
}

type ProxyType string

const (
	ProxyTypePassthrough ProxyType = "Passthrough"
)

// WorkspaceProxySpec is the specification of the Cluster proxy resource.
type WorkspaceProxySpec struct {
	Type ProxyType `json:"type,omitempty"`
}

// WorkspaceProxyStatus communicates the observed state of the Workspace proxy.
type WorkspaceProxyStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the cluster with standard Kubernetes
	// client libraries and command line tools via proxy.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// Phase of the cluster proxy (Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase WorkspaceProxyPhaseType `json:"phase,omitempty"`

	// A timestamp indicating when the proxy last reported status.
	// +optional
	LastProxyHeartbeatTime *v1.Time `json:"lastProxyHeartbeatTime,omitempty"`

	// Current processing state of the Cluster proxy.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// VirtualWorkspaces contains all virtual workspace URLs.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`

	// TunnelWorkspaces contains all URLs (one per shard) that point to the SyncTarget
	// workspace in order to setup the tunneler.
	// +optional
	TunnelWorkspaces []TunnelWorkspace `json:"tunnelWorkspaces,omitempty"`
}

// WorkspaceProxyPhaseType is the type of the current phase of the cluster proxy
//
// +kubebuilder:validation:Enum=Scheduling;Initializing;Ready
type WorkspaceProxyPhaseType string

const (
	WorkspaceProxyPhaseScheduling   WorkspaceProxyPhaseType = "Scheduling"
	WorkspaceProxyPhaseInitializing WorkspaceProxyPhaseType = "Initializing"
	WorkspaceProxyPhaseReady        WorkspaceProxyPhaseType = "Ready"
)

type TunnelWorkspace struct {
	// url is the URL the Proxy should use to connect
	// to the Proxy tunnel for a given shard.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

type VirtualWorkspace struct {
	// SyncerURL is the URL of the syncer virtual workspace.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	SyncerURL string `json:"syncerURL"`
}

// WorkspaceProxyList is a list of WorkspaceProxy resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceProxyList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []WorkspaceProxy `json:"items"`
}
