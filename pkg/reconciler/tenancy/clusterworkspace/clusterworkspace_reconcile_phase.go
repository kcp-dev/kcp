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
	"fmt"
	"sort"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

type phaseReconciler struct {
	getShardWithQuorum func(ctx context.Context, name string, options metav1.GetOptions) (*tenancyv1alpha1.ClusterWorkspaceShard, error)
	getAPIBindings     func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
}

func (r *phaseReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	switch workspace.Status.Phase {
	case "":
		workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseScheduling
	case tenancyv1alpha1.ClusterWorkspacePhaseScheduling:
		// TODO(sttts): in the future this step is done by a workspace shard itself. I.e. moving to initializing is a step
		//              of acceptance of the workspace on that shard.
		if workspace.Status.Location.Current != "" && workspace.Status.BaseURL != "" {
			// do final quorum read to avoid race when the workspace shard is being deleted
			_, err := r.getShardWithQuorum(ctx, workspace.Status.Location.Current, metav1.GetOptions{})
			if err != nil {
				// reschedule
				workspace.Status.Location.Current = ""
				workspace.Status.BaseURL = ""
				return reconcileStatusContinue, nil // nolint:nilerr
			}

			workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseInitializing
		}
	case tenancyv1alpha1.ClusterWorkspacePhaseInitializing:
		if len(workspace.Status.Initializers) > 0 {
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedInitializerExists, conditionsapi.ConditionSeverityInfo, "Initializers still exist: %v", workspace.Status.Initializers)
			return reconcileStatusContinue, nil
		}

		bindings, err := r.getAPIBindings(logicalcluster.From(workspace).Join(workspace.Name))
		if err != nil {
			return reconcileStatusContinue, err
		}
		var unbound []string
		for _, binding := range bindings {
			if !conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted) {
				unbound = append(unbound, fmt.Sprintf("%s:%s", binding.Name, conditions.GetReason(binding, apisv1alpha1.InitialBindingCompleted)))
			}
		}
		if len(unbound) > 0 {
			sort.Strings(unbound)
			conditions.MarkFalse(workspace, tenancyv1alpha1.WorkspaceInitialized, tenancyv1alpha1.WorkspaceInitializedAPIBindingNotBound, conditionsapi.ConditionSeverityInfo, "APIBindings not bound: %s", strings.Join(unbound, ", "))
			return reconcileStatusContinue, nil
		}

		workspace.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
		conditions.MarkTrue(workspace, tenancyv1alpha1.WorkspaceInitialized)
	}

	return reconcileStatusContinue, nil
}
