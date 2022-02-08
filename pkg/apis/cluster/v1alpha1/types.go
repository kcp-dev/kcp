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

// Cluster describes a member cluster.
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

var _ conditions.Getter = &Cluster{}
var _ conditions.Setter = &Cluster{}

// ClusterSpec holds the desired state of the Cluster (from the client).
type ClusterSpec struct {
	KubeConfig string `json:"kubeconfig"`
}

// ClusterStatus communicates the observed state of the Cluster (from the controller).
type ClusterStatus struct {

	// Allocatable represents the resources that are available for scheduling.
	// +optional
	Allocatable *corev1.ResourceList `json:"allocatable,omitempty"`

	// Capacity represents the total resources of the cluster.
	// +optional
	Capacity *corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the Cluster.
	// +optional
	Conditions ClusterConditions `json:"conditions,omitempty"`

	// +optional
	SyncedResources []string `json:"syncedResources,omitempty"`
}

// ClusterList is a list of Cluster resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []Cluster `json:"items"`
}

// Conditions and ConditionReasons for the kcp Cluster object.
const (
	// ClusterReadyCondition means the Cluster is available.
	ClusterReadyCondition conditionsv1alpha1.ConditionType = "Ready"

	// ClusterUnknownReason documents a Cluster which readyness is unknown.
	ClusterUnknownReason = "ClusterStatusUnknown"

	// ClusterReadyReason documents a Cluster that is ready.
	ClusterReadyReason = "ClusterReady"

	// ClusterNotReadyReason documents a Cluster is not ready, when the "readyz" check returns false.
	ClusterNotReadyReason = "ClusterNotReady"

	// ClusterUnreachableReason documents the Cluster state when the Syncer is unable to reach the Cluster "readyz" API endpoint
	ClusterUnreachableReason = "ClusterUnreachable"

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
)

func (in *Cluster) SetConditions(c conditionsv1alpha1.Conditions) {
	cc := ClusterConditions{}

	for _, c := range c {
		condition := ClusterCondition{
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
		if c.Type == ClusterReadyCondition && c.Status == corev1.ConditionTrue {
			condition.LastHeartbeatTime = metav1.Now()
		}
		cc = append(cc, condition)
	}

	in.Status.Conditions = cc
}

func (in *Cluster) GetConditions() conditionsv1alpha1.Conditions {
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

type ClusterCondition struct {
	*conditionsv1alpha1.Condition `json:",inline"`

	// Last time the condition got an update.
	// Can be used by the system to determine if the ConditionStatus is Unknown in certain cases.
	// +optional
	LastHeartbeatTime metav1.Time `json:"lastHeartbeatTime,omitempty"`
}

type ClusterConditions []ClusterCondition
