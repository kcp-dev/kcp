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

	// SyncTargetWorkspace is used to specify a workspace when a log is related to a SyncTarget.
	SyncTargetWorkspace = SyncTargetKeyPrefix + "workspace"
	// SyncTargetNamespace is used to specify a namespace when a log is related to a SyncTarget.
	SyncTargetNamespace = SyncTargetKeyPrefix + "namespace"
	// SyncTargetName is used to specify a name when a log is related to a SyncTarget.
	SyncTargetName = SyncTargetKeyPrefix + "name"
	// SyncTargetKey is used to specify the obfuscated key of a SyncTarget as used in the Syncer labels and annotations.
	SyncTargetKey = SyncTargetKeyPrefix + "key"

	// DownstreamKeyPrefix is the prefix used for all the keys related to a downstream object.
	DownstreamKeyPrefix = "downstream."

	// DownstreamNamespace is used to specify a namespace when a log is related to a downstream object.
	DownstreamNamespace = DownstreamKeyPrefix + "namespace"
	// DownstreamName is used to specify a name when a log is related to a downstream object.
	DownstreamName = DownstreamKeyPrefix + "name"
)
