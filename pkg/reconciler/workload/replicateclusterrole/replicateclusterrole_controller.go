/*
Copyright 2023 The KCP Authors.

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

package replicateclusterrole

import (
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"k8s.io/apimachinery/pkg/util/sets"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterroles"
	"github.com/kcp-dev/kcp/sdk/apis/workload"
)

const (
	ControllerName = "kcp-workloads-replicate-clusterrole"
)

// NewController returns a new controller for labelling ClusterRole that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) labelclusterroles.Controller {
	return labelclusterroles.NewController(
		ControllerName,
		workload.GroupName,
		HasSyncRule,
		func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool { return false },
		kubeClusterClient,
		clusterRoleInformer,
		clusterRoleBindingInformer,
	)
}

func HasSyncRule(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool {
	for _, rule := range cr.Rules {
		apiGroups := sets.New[string](rule.APIGroups...)
		if !apiGroups.Has(workload.GroupName) && !apiGroups.Has("*") {
			continue
		}
		resources := sets.New[string](rule.Resources...)
		verbs := sets.New[string](rule.Verbs...)
		if (resources.Has("synctargets") || resources.Has("*")) && (verbs.Has("sync") || verbs.Has("*")) {
			return true
		}
	}
	return false
}
