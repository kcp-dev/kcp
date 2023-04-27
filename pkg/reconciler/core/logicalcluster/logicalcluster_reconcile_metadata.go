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

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	changed := false

	expected := string(logicalCluster.Status.Phase)
	if !logicalCluster.DeletionTimestamp.IsZero() {
		expected = "Deleting"
	}
	if got := logicalCluster.Labels[tenancyv1alpha1.WorkspacePhaseLabel]; got != expected {
		if logicalCluster.Labels == nil {
			logicalCluster.Labels = map[string]string{}
		}
		logicalCluster.Labels[tenancyv1alpha1.WorkspacePhaseLabel] = expected
		changed = true
	}

	initializerKeys := sets.New[string]()
	for _, initializer := range logicalCluster.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		initializerKeys.Insert(key)
		if got, expected := logicalCluster.Labels[key], value; got != expected {
			if logicalCluster.Labels == nil {
				logicalCluster.Labels = map[string]string{}
			}
			logicalCluster.Labels[key] = value
			changed = true
		}
	}

	for key := range logicalCluster.Labels {
		if strings.HasPrefix(key, tenancyv1alpha1.WorkspaceInitializerLabelPrefix) {
			if !initializerKeys.Has(key) {
				delete(logicalCluster.Labels, key)
				changed = true
			}
		}
	}

	if logicalCluster.Status.Phase == corev1alpha1.LogicalClusterPhaseReady {
		// remove owner reference
		if value, found := logicalCluster.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey]; found {
			var info authenticationv1.UserInfo
			err := json.Unmarshal([]byte(value), &info)
			if err != nil {
				logger.WithValues(tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey, value).Error(err, "failed to unmarshal workspace owner annotation")
				delete(logicalCluster.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if userOnlyValue, err := json.Marshal(authenticationv1.UserInfo{Username: info.Username}); err != nil {
				// should never happen
				logger.Error(err, "failed to marshal user info")
				delete(logicalCluster.Annotations, tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey)
				changed = true
			} else if value != string(userOnlyValue) {
				logicalCluster.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = string(userOnlyValue)
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
