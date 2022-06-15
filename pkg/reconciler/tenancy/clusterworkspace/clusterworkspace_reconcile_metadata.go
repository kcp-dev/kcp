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
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type metaDataReconciler struct {
}

func (r *metaDataReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	if workspace.Labels == nil {
		workspace.Labels = map[string]string{}
	}
	workspace.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel] = string(workspace.Status.Phase)

	initializerKeys := sets.NewString()
	for _, initializer := range workspace.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		initializerKeys.Insert(key)
		workspace.Labels[key] = value
	}

	for key := range workspace.Labels {
		if strings.HasPrefix(key, tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix) {
			if !initializerKeys.Has(key) {
				delete(workspace.Labels, key)
			}
		}
	}

	return reconcileStatusContinue, nil
}

func initializerLabelFor(initializer tenancyv1alpha1.ClusterWorkspaceInitializer) string {
	return string(tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix + initializer)
}
