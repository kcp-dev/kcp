/*
Copyright 2024 The KCP Authors.

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

package vcluster

import (
	"context"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

const finalizerName = "helm.mounts.contrib.kcp.io/finalizer"

// finalizerReconciler is a reconciler which adds finalizer to the mount.
type finalizerReconciler struct{}

func (r *finalizerReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.VCluster) (reconcileStatus, error) {
	if mount.DeletionTimestamp != nil {
		for _, c := range mount.Status.Conditions {
			if c.Type == mountsv1alpha1.ClusterDeleted || c.Status == corev1.ConditionTrue {
				// delete the finalizer
				mount.Finalizers = nil
				return reconcileStatusStopAndRequeue, nil
			}
		}
	}
	if containsString(mount.Finalizers, finalizerName) {
		return reconcileStatusContinue, nil
	}
	mount.Finalizers = append(mount.Finalizers, finalizerName)
	return reconcileStatusStopAndRequeue, nil
}

func containsString(slice []string, s string) bool {
	for _, e := range slice {
		if e == s {
			return true
		}
	}
	return false
}
