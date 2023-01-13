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

package replicationclusterrole

import (
	"context"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpcorehelper "github.com/kcp-dev/kcp/pkg/apis/core/helper"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

func (c *controller) reconcile(ctx context.Context, rb *rbacv1.ClusterRole) (bool, error) {
	r := &reconciler{
		getReferencingClusterRoleBindings: func(cluster logicalcluster.Name, name string) ([]*rbacv1.ClusterRoleBinding, error) {
			key := kcpcache.ToClusterAwareKey(cluster.String(), "", name)
			return indexers.ByIndex[*rbacv1.ClusterRoleBinding](c.clusterRoleBindingIndexer, ClusterRoleBindingByClusterRoleName, key)
		},
	}
	return r.reconcile(ctx, rb)
}

type reconciler struct {
	getReferencingClusterRoleBindings func(cluster logicalcluster.Name, name string) ([]*rbacv1.ClusterRoleBinding, error)
}

func (r *reconciler) reconcile(ctx context.Context, cr *rbacv1.ClusterRole) (bool, error) {
	replicate := HasBindOrContentRule(cr)
	if !replicate {
		objs, err := r.getReferencingClusterRoleBindings(logicalcluster.From(cr), cr.Name)
		if err != nil {
			runtime.HandleError(err)
			return false, nil // nothing we can do
		}
		for _, crb := range objs {
			if HasMaximalPermissionClaimSubject(crb) {
				replicate = true
				break
			}
		}
	}

	if replicate {
		cr.Annotations, _ = kcpcorehelper.ReplicateFor(cr.Annotations, "apis.kcp.io")
	} else {
		cr.Annotations, _ = kcpcorehelper.DontReplicateFor(cr.Annotations, "apis.kcp.io")
	}

	return false, nil
}

func HasBindOrContentRule(cr *rbacv1.ClusterRole) bool {
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

func HasMaximalPermissionClaimSubject(crb *rbacv1.ClusterRoleBinding) bool {
	for _, s := range crb.Subjects {
		if strings.HasPrefix(s.Name, apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix) && (s.Kind == rbacv1.UserKind || s.Kind == rbacv1.GroupKind) && s.APIGroup == rbacv1.GroupName {
			return true
		}
	}
	return false
}
