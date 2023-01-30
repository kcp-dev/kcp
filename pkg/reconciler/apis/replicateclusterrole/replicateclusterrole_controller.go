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
	"strings"

	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterroles"
)

const (
	ControllerName = "kcp-apis-replicate-clusterrole"
)

// NewController returns a new controller for labelling ClusterRole that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
) labelclusterroles.Controller {
	return labelclusterroles.NewController(
		ControllerName,
		apis.GroupName,
		HasBindOrContentRule,
		HasMaximalPermissionClaimSubject,
		kubeClusterClient,
		clusterRoleInformer,
		clusterRoleBindingInformer,
	)
}

func HasBindOrContentRule(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool {
	for _, rule := range cr.Rules {
		apiGroups := sets.NewString(rule.APIGroups...)
		if !apiGroups.Has(apis.GroupName) && !apiGroups.Has("*") {
			continue
		}
		resources := sets.NewString(rule.Resources...)
		verbs := sets.NewString(rule.Verbs...)
		if (resources.Has("apiexports") || resources.Has("*")) && (verbs.Has("bind") || verbs.Has("*")) {
			return true
		}
		if resources.Has("apiexports/content") || resources.Has("*") {
			return true
		}
	}
	return false
}

func HasMaximalPermissionClaimSubject(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool {
	for _, s := range crb.Subjects {
		if strings.HasPrefix(s.Name, apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix) && (s.Kind == rbacv1.UserKind || s.Kind == rbacv1.GroupKind) && s.APIGroup == rbacv1.GroupName {
			return true
		}
	}
	return false
}
