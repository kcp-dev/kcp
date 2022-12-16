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

package logicalcluster

import (
	"context"
	"encoding/json"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(ctx context.Context, workspace *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	changed := false

	expected := string(workspace.Status.Phase)
	if !workspace.DeletionTimestamp.IsZero() {
		expected = "Deleting"
	}
	if got := workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel]; got != expected {
		if workspace.Labels == nil {
			workspace.Labels = map[string]string{}
		}
		workspace.Labels[tenancyv1alpha1.WorkspacePhaseLabel] = expected
		changed = true
	}

	initializerKeys := sets.NewString()
	for _, initializer := range workspace.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		initializerKeys.Insert(key)
		if got, expected := workspace.Labels[key], value; got != expected {
			if workspace.Labels == nil {
				workspace.Labels = map[string]string{}
			}
			workspace.Labels[key] = value
			changed = true
		}
	}

	for key := range workspace.Labels {
		if strings.HasPrefix(key, tenancyv1alpha1.WorkspaceInitializerLabelPrefix) {
			if !initializerKeys.Has(key) {
				delete(workspace.Labels, key)
				changed = true
			}
		}
	}

	if workspace.Status.Phase == corev1alpha1.LogicalClusterPhaseReady {
		// remove owner reference
		if value, found := workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
			var info authenticationv1.UserInfo
			err := json.Unmarshal([]byte(value), &info)
			if err != nil {
				logger.Error(err, "failed to unmarshal workspace owner annotation", tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, value)
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if userOnlyValue, err := json.Marshal(authenticationv1.UserInfo{Username: info.Username}); err != nil {
				// should never happen
				logger.Error(err, "failed to marshal user info")
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if value != string(userOnlyValue) {
				workspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = string(userOnlyValue)
				changed = true
			}
		}
	}

	if changed {
		// first update ObjectMeta before status
		return reconcileStatusStopAndRequeue, nil
	}

	return reconcileStatusContinue, nil
}
