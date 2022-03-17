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

	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// WorkloadCluster describes a member cluster capable of running workloads.
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

type WorkloadCluster struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec WorkloadClusterSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status WorkloadClusterStatus `json:"status,omitempty"`
}

var _ conditions.Getter = &WorkloadCluster{}
var _ conditions.Setter = &WorkloadCluster{}

// WorkloadClusterSpec holds the desired state of the WorkloadCluster (from the client).
type WorkloadClusterSpec struct {
	KubeConfig string `json:"kubeconfig"`

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
}

// WorkloadClusterStatus communicates the observed state of the WorkloadCluster (from the controller).
type WorkloadClusterStatus struct {

	// Allocatable represents the resources that are available for scheduling.
	// +optional
	Allocatable *corev1.ResourceList `json:"allocatable,omitempty"`

	// Capacity represents the total resources of the cluster.
	// +optional
	Capacity *corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the WorkloadCluster.
	// +optional
	Conditions WorkloadClusterConditions `json:"conditions,omitempty"`

	// +optional
	SyncedResources []string `json:"syncedResources,omitempty"`

	// LastHeartbeat represents the last time the cluster's syncer successfully made a request to update this value.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`
}

// WorkloadClusterList is a list of WorkloadCluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkloadClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []WorkloadCluster `json:"items"`
}

// Conditions and ConditionReasons for the kcp WorkloadCluster object.
const (
	// WorkloadClusterReadyCondition means the WorkloadCluster is available.
	WorkloadClusterReadyCondition conditionsv1alpha1.ConditionType = "Ready"

	// WorkloadClusterUnknownReason documents a WorkloadCluster which readyness is unknown.
	WorkloadClusterUnknownReason = "WorkloadClusterStatusUnknown"

	// WorkloadClusterReadyReason documents a WorkloadCluster that is ready.
	WorkloadClusterReadyReason = "WorkloadClusterReady"

	// WorkloadClusterNotReadyReason documents a WorkloadCluster is not ready, when the "readyz" check returns false.
	WorkloadClusterNotReadyReason = "WorkloadClusterNotReady"

	// WorkloadClusterUnreachableReason documents the WorkloadCluster state when the Syncer is unable to reach the WorkloadCluster "readyz" API endpoint
	WorkloadClusterUnreachableReason = "WorkloadClusterUnreachable"

	// ErrorStartingSyncerReason indicates that the Syncer failed to start.
	ErrorStartingSyncerReason = "ErrorStartingSyncer"

	// ErrorInstallingSyncerReason indicates that the Syncer failed to install.
	ErrorInstallingSyncerReason = "ErrorInstallingSyncer"

	// InvalidKubeConfigReason indicates that the Syncer failed to start because the KubeConfig is invalid.
	InvalidKubeConfigReason = "InvalidKubeConfig"

	// ErrorCreatingClientReason indicates that there has been an error trying to create a kubernetes client from given a KubeConfig.
	ErrorCreatingClientReason = "ErrorCreatingClient"

	// ErrorStartingAPIImporterReason indicates an error starting the API Importer.
	ErrorStartingAPIImporterReason = "ErrorStartingAPIImporter"

	// ErrorHeartbeatMissedReason indicates that a heartbeat update was not received within the configured threshold.
	ErrorHeartbeatMissedReason = "ErrorHeartbeat"
)

func (in *WorkloadCluster) SetConditions(c conditionsv1alpha1.Conditions) {
	cc := WorkloadClusterConditions{}

	for _, c := range c {
		condition := WorkloadClusterCondition{
			Condition: &conditionsv1alpha1.Condition{
				Type:               c.Type,
				Status:             c.Status,
				Severity:           c.Severity,
				LastTransitionTime: c.LastTransitionTime,
				Reason:             c.Reason,
				Message:            c.Message,
			},
			LastHeartbeatTime: metav1.Time{},
		}
		if c.Type == WorkloadClusterReadyCondition && c.Status == corev1.ConditionTrue {
			condition.LastHeartbeatTime = metav1.Now()
		}
		cc = append(cc, condition)
	}

	in.Status.Conditions = cc
}

func (in *WorkloadCluster) GetConditions() conditionsv1alpha1.Conditions {
	cond := conditionsv1alpha1.Conditions{}

	for _, condition := range in.Status.Conditions {
		cond = append(cond, conditionsv1alpha1.Condition{
			Type:               condition.Type,
			Status:             condition.Status,
			Reason:             condition.Reason,
			Message:            condition.Message,
			Severity:           condition.Severity,
			LastTransitionTime: condition.LastTransitionTime,
		})
	}

	return cond
}

type WorkloadClusterCondition struct {
	*conditionsv1alpha1.Condition `json:",inline"`

	// Last time the condition got an update.
	// Can be used by the system to determine if the ConditionStatus is Unknown in certain cases.
	// +optional
	LastHeartbeatTime metav1.Time `json:"lastHeartbeatTime,omitempty"`
}

type WorkloadClusterConditions []WorkloadClusterCondition
