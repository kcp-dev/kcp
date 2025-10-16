/*
Copyright 2025 The KCP Authors.

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

package logicalcluster

import (
	"context"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

const LogicalClusterHasTerminatorFinalizer = "kcp.io/has-terminators"

// terminatorReconciler will place the LogicalClusterHasTerminatorFinalizer finalizer on the LogicalCluster
// in order to prevent a logicalcluster from being deleted while it still has terminators set.
type terminatorReconciler struct{}

func (r *terminatorReconciler) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	var changed bool
	// if there are still terminators, ensure that the finalizer is present
	if len(logicalCluster.Status.Terminators) != 0 {
		logicalCluster.Finalizers, changed = addUnique(logicalCluster.Finalizers, LogicalClusterHasTerminatorFinalizer)
		// if not make sure that it is removed
	} else {
		logicalCluster.Finalizers, changed = removeByValue(logicalCluster.Finalizers, LogicalClusterHasTerminatorFinalizer)
	}

	if changed {
		// first update ObjectMeta before other reconcilers change status
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}

// addUnique adds t to s if not already present, returning the new slice and whether it was added.
func addUnique[T comparable](s []T, t T) ([]T, bool) {
	for _, elem := range s {
		if elem == t {
			return s, false
		}
	}
	return append(s, t), true
}

// removeByValue removes t from s if present, returning the new slice and whether it was removed.
func removeByValue[T comparable](s []T, t T) ([]T, bool) {
	for i, other := range s {
		if other == t {
			return append(s[:i], s[i+1:]...), true
		}
	}
	return s, false
}
