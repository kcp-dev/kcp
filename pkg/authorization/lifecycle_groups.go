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
	"slices"
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

// isRegisteredInitializer returns true if a group names an initializer that the
// LogicalCluster has registered in Status.Initializers.
func isRegisteredInitializer(groups []string, logicalCluster *corev1alpha1.LogicalCluster) bool {
	for _, group := range groups {
		name, found := strings.CutPrefix(group, bootstrap.SystemKcpInitializerGroupPrefix)
		if !found {
			continue
		}
		if slices.ContainsFunc(
			logicalCluster.Status.Initializers,
			func(i corev1alpha1.LogicalClusterInitializer) bool {
				return string(i) == name
			},
		) {
			return true
		}
	}
	return false
}

// TerminatorGroup returns the synthetic group that the terminating virtual workspace's
// content proxy injects when it has already authorized a request against the
// WorkspaceType's terminatorPermissions.
func TerminatorGroup(terminator corev1alpha1.LogicalClusterTerminator) string {
	return bootstrap.SystemKcpTerminatorGroupPrefix + string(terminator)
}

// isRegisteredTerminator returns true if a group names a terminator that the LogicalCluster
// has registered in Status.Terminators.
func isRegisteredTerminator(groups []string, logicalCluster *corev1alpha1.LogicalCluster) bool {
	for _, group := range groups {
		name, found := strings.CutPrefix(group, bootstrap.SystemKcpTerminatorGroupPrefix)
		if !found {
			continue
		}
		if slices.ContainsFunc(
			logicalCluster.Status.Terminators,
			func(t corev1alpha1.LogicalClusterTerminator) bool {
				return string(t) == name
			},
		) {
			return true
		}
	}
	return false
}

// hasLifecycleGroup returns true if any of the supplied groups is a synthetic
// initializer or terminator group injected by a lifecycle VW content proxy.
func hasLifecycleGroup(groups []string) bool {
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
