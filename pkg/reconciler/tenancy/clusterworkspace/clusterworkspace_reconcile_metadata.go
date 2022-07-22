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

package clusterworkspace

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	changed := false
	if got, expected := workspace.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel], string(workspace.Status.Phase); got != expected {
		if workspace.Labels == nil {
			workspace.Labels = map[string]string{}
		}
		workspace.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel] = expected
		changed = true
	}

	initializerKeys := sets.NewString()
	for _, initializer := range workspace.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		initializerKeys.Insert(key)
		if got, expected := workspace.Labels[key], value; got != expected {
			workspace.Labels[key] = value
			changed = true
		}
	}

	for key := range workspace.Labels {
		if strings.HasPrefix(key, tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix) {
			if !initializerKeys.Has(key) {
				delete(workspace.Labels, key)
				changed = true
			}
		}
	}

	if workspace.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady {
		if value, found := workspace.Annotations[tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey]; found {
			var info authenticationv1.UserInfo
			err := json.Unmarshal([]byte(value), &info)
			if err != nil {
				klog.Warningf("Failed to unmarshal ClusterWorkspace %s|%s annotation %s=%q: %v", logicalcluster.From(workspace), workspace.Name, tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey, value, err)
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey)
				changed = true
			} else if userOnlyValue, err := json.Marshal(authenticationv1.UserInfo{Username: info.Username}); err != nil {
				// should never happen
				klog.Warningf("Failed to marshal ClusterWorkspace %s|%s user info: %v", logicalcluster.From(workspace), workspace.Name, err)
				delete(workspace.Annotations, tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey)
				changed = true
			} else if value != string(userOnlyValue) {
				workspace.Annotations[tenancyv1alpha1.ExperimentalClusterWorkspaceOwnerAnnotationKey] = string(userOnlyValue)
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
