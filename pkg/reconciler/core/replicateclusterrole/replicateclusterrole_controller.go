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

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/pkg/apis/core"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterroles"
)

const (
	ControllerName = "kcp-core-replicate-clusterrole"
)

// NewController returns a new controller for labelling ClusterRole that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) (labelclusterroles.Controller, error) {
	return labelclusterroles.NewController(
		ControllerName,
		core.GroupName,
		HasAccessRule,
		func(crb *rbacv1.ClusterRoleBinding) bool { return false },
		kubeClusterClient,
		clusterRoleInformer,
		clusterRoleBindingInformer,
	)
}

func HasAccessRule(cr *rbacv1.ClusterRole) bool {
	for _, rule := range cr.Rules {
		nonResources := sets.NewString(rule.NonResourceURLs...)
		verbs := sets.NewString(rule.Verbs...)
		if (nonResources.Has("/") || nonResources.Has("*") || nonResources.Has("/*")) && (verbs.Has("access") || verbs.Has("*")) {
			return true
		}
	}
	return false
}
