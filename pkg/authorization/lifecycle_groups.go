/*
Copyright 2026 The kcp Authors.

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

package authorization

import (
	"strings"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// InitializerGroup returns the synthetic group that the initializing virtual workspace's
// content proxy injects when it has already authorized a request against the
// WorkspaceType's initializerPermissions.
func InitializerGroup(initializer corev1alpha1.LogicalClusterInitializer) string {
	return bootstrap.SystemKcpInitializerGroupPrefix + string(initializer)
}

// TerminatorGroup returns the synthetic group that the terminating virtual workspace's
// content proxy injects when it has already authorized a request against the
// WorkspaceType's terminatorPermissions.
func TerminatorGroup(terminator corev1alpha1.LogicalClusterTerminator) string {
	return bootstrap.SystemKcpTerminatorGroupPrefix + string(terminator)
}

// HasLifecycleGroup returns true if any of the supplied groups is a synthetic
// initializer/terminator group injected by a lifecycle VW content proxy. The presence
// of such a group is a "pre-authorized by VW" marker; clients cannot forge it because
// the front-proxy strips these prefixes from all incoming requests.
func HasLifecycleGroup(groups []string) bool {
	for _, g := range groups {
		if isLifecycleGroup(g) {
			return true
		}
	}
	return false
}

// isLifecycleGroup returns true if the group has either the initializer or terminator
// synthetic prefix.
func isLifecycleGroup(group string) bool {
	return strings.HasPrefix(group, bootstrap.SystemKcpInitializerGroupPrefix) ||
		strings.HasPrefix(group, bootstrap.SystemKcpTerminatorGroupPrefix)
}
