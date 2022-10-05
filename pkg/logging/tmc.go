/*
Copyright 2022 The KCP Authors.

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

// Package logging supplies common constants to ensure consistent use of structured logs.
package logging

const (
	// SyncTargetKeyPrefix is the prefix used for all the keys related to a SyncTarget.
	SyncTargetKeyPrefix = "syncTarget."

	// SyncTargetWorkspaceKey is used to specify a workspace when a log is related to a SyncTarget.
	SyncTargetWorkspaceKey = SyncTargetKeyPrefix + "workspace"
	// SyncTargetNamespaceKey is used to specify a namespace when a log is related to a SyncTarget.
	SyncTargetNamespaceKey = SyncTargetKeyPrefix + "namespace"
	// SyncTargetNameKey is used to specify a name when a log is related to a SyncTarget.
	SyncTargetNameKey = SyncTargetKeyPrefix + "name"

	// DownstreamKeyPrefix is the prefix used for all the keys related to a downstream object.
	DownstreamKeyPrefix = "downstream."

	// DownstreamNamespaceKey is used to specify a namespace when a log is related to a downstream object.
	DownstreamNamespaceKey = DownstreamKeyPrefix + "namespace"
	// DownstreamNameKey is used to specify a name when a log is related to a downstream object.
	DownstreamNameKey = DownstreamKeyPrefix + "name"
)
