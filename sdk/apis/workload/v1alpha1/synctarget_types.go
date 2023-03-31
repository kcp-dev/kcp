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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// SyncTarget describes a member cluster capable of running workloads.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Location",type="string",JSONPath=`.metadata.name`,priority=1
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=`.status.conditions[?(@.type=="Ready")].status`,priority=2
// +kubebuilder:printcolumn:name="Synced API resources",type="string",JSONPath=`.status.syncedResources`,priority=3
// +kubebuilder:printcolumn:name="Key",type="string",JSONPath=`.metadata.labels['internal\.workload\.kcp\.dev/key']`,priority=4
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SyncTarget struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec SyncTargetSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status SyncTargetStatus `json:"status,omitempty"`
}

var _ conditions.Getter = &SyncTarget{}
var _ conditions.Setter = &SyncTarget{}

// SyncTargetSpec holds the desired state of the SyncTarget (from the client).
type SyncTargetSpec struct {
	// Unschedulable controls cluster schedulability of new workloads. By
	// default, cluster is schedulable.
	// +optional
	// +kubebuilder:default=false
	Unschedulable bool `json:"unschedulable"`

	// EvictAfter controls cluster schedulability of new and existing workloads.
	// After the EvictAfter time, any workload scheduled to the cluster
	// will be unassigned from the cluster.
	// By default, workloads scheduled to the cluster are not evicted.
	EvictAfter *metav1.Time `json:"evictAfter,omitempty"`

	// SupportedAPIExports defines a set of APIExports supposed to be supported by this SyncTarget. The SyncTarget
	// will be selected to deploy the workload only when the resource schema on the SyncTarget is compatible
	// with the resource schema included in the exports.
	SupportedAPIExports []tenancyv1alpha1.APIExportReference `json:"supportedAPIExports,omitempty"`

	// Cells is a set of labels to identify the cells the SyncTarget belongs to. SyncTargets with the same cells run as
	// they are in the same physical cluster. Each key/value pair in the cells should be added and updated by service providers
	// (i.e. a network provider updates one key/value, while the storage provider updates another.)
	Cells map[string]string `json:"cells,omitempty"`
}

// SyncTargetStatus communicates the observed state of the SyncTarget (from the controller).
type SyncTargetStatus struct {

	// Allocatable represents the resources that are available for scheduling.
	// +optional
	Allocatable *corev1.ResourceList `json:"allocatable,omitempty"`

	// Capacity represents the total resources of the cluster.
	// +optional
	Capacity *corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the SyncTarget.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// SyncedResources represents the resources that the syncer of the SyncTarget can sync. It MUST be
	// updated by kcp server.
	// +optional
	SyncedResources []ResourceToSync `json:"syncedResources,omitempty"`

	// A timestamp indicating when the syncer last reported status.
	// +optional
	LastSyncerHeartbeatTime *metav1.Time `json:"lastSyncerHeartbeatTime,omitempty"`

	// VirtualWorkspaces contains all virtual workspace URLs.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

type ResourceToSync struct {
	apisv1alpha1.GroupResource `json:","`

	// versions are the resource versions the syncer can choose to sync depending on
	// availability on the downstream cluster. Conversion to the storage version, if necessary,
	// will be done on the kcp side. The versions are ordered by precedence and the
	// first version compatible is preferred by syncer.
	// +kubebuilder:validation:MinItems=1
	// +required
	// +kubebuilder:validation:Required
	Versions []string `json:"versions"`

	// identityHash is the identity for a given APIExport that the APIResourceSchema belongs to.
	// The hash can be found on APIExport and APIResourceSchema's status.
	// It will be empty for core types.
	// +optional
	IdentityHash string `json:"identityHash"`

	// state indicate whether the resources schema is compatible to the SyncTarget. It must be updated
	// by syncer after checking the API compatibility on SyncTarget.
	// +kubebuilder:validation:Enum=Pending;Accepted;Incompatible
	// +kubebuilder:default=Pending
	// +optional
	State ResourceCompatibleState `json:"state,omitempty"`

	// A human readable message indicating details about the compatibility errors.
	// This field may be empty.
	// +optional
	Message string `json:"message,omitempty"`
}

type ResourceCompatibleState string

const (
	// ResourceSchemaPendingState is the initial state indicating that the syncer has not report compatibility of the resource.
	ResourceSchemaPendingState = "Pending"
	// ResourceSchemaAcceptedState is the state that the resource schema is compatible and can be synced by syncer.
	ResourceSchemaAcceptedState = "Accepted"
	// ResourceSchemaIncompatibleState is the state that the resource schema is incompatible for syncer.
	ResourceSchemaIncompatibleState = "Incompatible"
)

type VirtualWorkspace struct {
	// SyncerURL is the URL of the syncer virtual workspace.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	SyncerURL string `json:"syncerURL"`

	// UpsyncerURL is the URL of the upsyncer virtual workspace.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	UpsyncerURL string `json:"upsyncerURL"`
}

// SyncTargetList is a list of SyncTarget resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SyncTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []SyncTarget `json:"items"`
}

// ImportedAPISExportName is singleton name of compute service exports in location workspace.
const ImportedAPISExportName = "imported-apis"

// Conditions and ConditionReasons for the kcp SyncTarget object.
const (
	// SyncerReady means the syncer is ready to transfer resources between KCP and the SyncTarget.
	SyncerReady conditionsv1alpha1.ConditionType = "SyncerReady"

	// APIImporterReady means the APIImport component is ready to import APIs from the SyncTarget.
	APIImporterReady conditionsv1alpha1.ConditionType = "APIImporterReady"

	// HeartbeatHealthy means the HeartbeatManager has seen a heartbeat for the SyncTarget within the expected interval.
	HeartbeatHealthy conditionsv1alpha1.ConditionType = "HeartbeatHealthy"

	// SyncerAuthorized means the syncer is authorized to sync resources to downstream cluster.
	SyncerAuthorized conditionsv1alpha1.ConditionType = "SyncerAuthorized"

	// ErrorHeartbeatMissedReason indicates that a heartbeat update was not received within the configured threshold.
	ErrorHeartbeatMissedReason = "ErrorHeartbeat"
)

func (in *SyncTarget) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

func (in *SyncTarget) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}
