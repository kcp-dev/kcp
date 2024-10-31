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

package kubecluster

import (
	"context"
	"net/url"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

// delegatedReconciler is a reconciler reconciles mounts, which are delegated and
// has secrets string set.
type delegatedReconciler struct {
	getState func(key string) (state.Value, bool)
}

func (r *delegatedReconciler) reconcile(ctx context.Context, mount *mountsv1alpha1.KubeCluster) (reconcileStatus, error) {
	if mount.Spec.Mode != mountsv1alpha1.KubeClusterModeDelegated {
		return reconcileStatusContinue, nil
	}

	if mount.Spec.SecretString == nil || *mount.Spec.SecretString == "" {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    tenancyv1alpha1.MountConditionReady,
			Status:  corev1.ConditionFalse,
			Message: "SecretString is not set",
		})
	}

	v, found := r.getState(*mount.Spec.SecretString)
	if v.Client == nil || !found {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    tenancyv1alpha1.MountConditionReady,
			Status:  corev1.ConditionFalse,
			Message: "Failed to get client. Most likely the secret is wrong.",
		})
		return reconcileAfterRequeue, nil
	}

	_, err := v.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		conditions.Set(mount, &conditionsapi.Condition{
			Type:    tenancyv1alpha1.MountConditionReady,
			Status:  corev1.ConditionFalse,
			Message: "Failed to health check",
		})
		return reconcileAfterRequeue, nil
	}

	conditions.Set(mount, &conditionsapi.Condition{
		Type:   tenancyv1alpha1.MountConditionReady,
		Status: corev1.ConditionTrue,
	})
	mount.Status.Phase = tenancyv1alpha1.MountPhaseReady

	// secrets string will be purged at VW side.
	// TOOD: This is a temporary solution. We need to find a better way to handle this.
	full, err := url.JoinPath(v.URL, "secret", *mount.Spec.SecretString)
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}
	mount.Status.URL = full

	return reconcileStatusContinue, nil
}
