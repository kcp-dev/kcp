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
	// ProxyTargetKeyPrefix is the prefix used for all the keys related to a ProxyTarget.
	ProxyTargetKeyPrefix = "proxyTarget."

	// ProxyTargetWorkspace is used to specify a workspace when a log is related to a ProxyTarget.
	ProxyTargetWorkspace = ProxyTargetKeyPrefix + "workspace"
	// ProxyTargetNamespace is used to specify a namespace when a log is related to a ProxyTarget.
	ProxyTargetNamespace = ProxyTargetKeyPrefix + "namespace"
	// ProxyTargetName is used to specify a name when a log is related to a ProxyTarget.
	ProxyTargetName = ProxyTargetKeyPrefix + "name"
	// ProxyTargetKey is used to specify the obfuscated key of a ProxyTarget as used in the Syncer labels and annotations.
	ProxyTargetKey = ProxyTargetKeyPrefix + "key"

	// DownstreamKeyPrefix is the prefix used for all the keys related to a downstream object.
	DownstreamKeyPrefix = "downstream."

	// DownstreamNamespace is used to specify a namespace when a log is related to a downstream object.
	DownstreamNamespace = DownstreamKeyPrefix + "namespace"
	// DownstreamName is used to specify a name when a log is related to a downstream object.
	DownstreamName = DownstreamKeyPrefix + "name"
)
