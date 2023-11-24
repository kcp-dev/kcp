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

	corev1 "k8s.io/api/core/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// Mount is a workspace mount that can be used to mount a workspace into another workspace or resource.
// Mounting itself is done at front proxy level.
type Mount struct {
	// Name is the name of the mount.
	Name string `json:"name,omitempty"`
	// Source is an ObjectReference to the object that is mounted.
	Source *corev1.ObjectReference `json:"ref,omitempty"`
}

// MountStatus is the status of a mount. It is used to indicate the status of a mount,
// potentially managed outside of the core API.
type MountStatus struct {
	// Name is the name of the mount.
	Name string `json:"name,omitempty"`
	// Phase of the mount (Scheduling, Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase corev1alpha1.LogicalClusterPhaseType `json:"phase,omitempty"`
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
		return nil, nil
	}
	var mount Mount
	return &mount, json.Unmarshal([]byte(value), &mount)
}

// String returns the string representation of the mount.
func (m *Mount) String() string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err) // :'( but will go away once it graduates from annotation to spec
	}
	return string(b)
}
