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
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

// MountKind is a kind of mount.
type MountKind string

const (
	SymLink MountKind = "symlink"
)

// Mount is a workspace mount that can be used to mount a workspace into another workspace or resource.
// Mounting itself is done at front proxy level.
type Mount struct {
	// Type is the type of the mount.
	Kind MountKind `json:"kind,omitempty"`
	// Source is an ObjectReference to the object that is mounted.
	Source *corev1.ObjectReference `json:"ref,omitempty"`
	// Path is the path where the object is mounted in relation to the workspace root.
	Path string `json:"path,omitempty"`
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
