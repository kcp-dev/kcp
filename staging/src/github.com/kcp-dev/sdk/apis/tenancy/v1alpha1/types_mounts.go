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
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
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
