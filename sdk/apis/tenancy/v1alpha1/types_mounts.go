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
	"encoding/json"
	"fmt"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// MountPhaseType is the type of the current phase of the mount (Initializing, Connecting, Ready, Unknown).
//
// +kubebuilder:validation:Enum=Initializing;Connecting;Ready;Unknown
type MountPhaseType string

const (
	// MountPhaseInitializing means the cluster proxy is being initialized.
	MountPhaseInitializing MountPhaseType = "Initializing"
	// MountPhaseConnecting means the cluster proxy is waiting for the agent to connect.
	MountPhaseConnecting MountPhaseType = "Connecting"
	// MountPhaseReady means the cluster proxy is ready, and agent connected.
	MountPhaseReady MountPhaseType = "Ready"
	// MountPhaseUnknown means the cluster proxy status is unknown.
	MountPhaseUnknown MountPhaseType = "Unknown"
)

const (
	// MountConditionReady is the condition type for MountReady.
	MountConditionReady conditionsv1alpha1.ConditionType = "WorkspaceMountReady"

	// MountAnnotationInvalidReason is the reason for the mount annotation being invalid.
	MountAnnotationInvalidReason = "MountAnnotationInvalid"
	// MountObjectNotFoundReason is the reason for the mount object not being found.
	MountObjectNotFoundReason = "MountObjectNotFound"
	// MountObjectNotReadyReason is the reason for the mount object not being in ready phase.
	MountObjectNotReadyReason = "MountObjectNotReady"
)

// Mount is a workspace mount that can be used to mount a workspace into another workspace or resource.
// Mounting itself is done at front proxy level.
type Mount struct {
	// MountSpec is the spec of the mount.
	MountSpec MountSpec `json:"spec,omitempty"`
	// MountStatus is the status of the mount.
	MountStatus MountStatus `json:"status,omitempty"`
}

type MountSpec struct {
	// Reference is an ObjectReference to the object that is mounted.
	Reference *ObjectReference `json:"ref,omitempty"`
}

type ObjectReference struct {
	// APIVersion is the API group and version of the object.
	APIVersion string `json:"apiVersion"`
	// Kind is the kind of the object.
	Kind string `json:"kind"`
	// Name is the name of the object.
	Name string `json:"name"`
	// Namespace is the namespace of the object.
	Namespace string `json:"namespace,omitempty"`
}

// MountStatus is the status of a mount. It is used to indicate the status of a mount,
// potentially managed outside of the core API.
type MountStatus struct {
	// Phase of the mount (Initializing, Connecting, Ready, Unknown).
	//
	// +kubebuilder:default=Initializing
	Phase MountPhaseType `json:"phase,omitempty"`
	// Conditions is a list of conditions and their status.
	// Current processing state of the Mount.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// URL is the URL of the mount. Mount is considered mountable when URL is set.
	// +optional
	URL string `json:"url,omitempty"`
}

// ParseTenancyMountAnnotation parses the value of the annotation into a Mount.
func ParseTenancyMountAnnotation(value string) (*Mount, error) {
	if value == "" {
		return nil, fmt.Errorf("mount annotation is empty")
	}
	var mount Mount
	err := json.Unmarshal([]byte(value), &mount)
	return &mount, err
}

// String returns the string representation of the mount.
func (m *Mount) String() string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err) // :'( but will go away once it graduates from annotation to spec
	}
	return string(b)
}
