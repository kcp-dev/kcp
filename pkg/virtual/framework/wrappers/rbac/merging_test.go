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

package rbac

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_copyAndMergeRoles(t *testing.T) {
	cache := []*rbacv1.Role{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "merge-test",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"list", "update", "delete"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "merge-test",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"create", "get", "watch"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-rule",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"create", "get", "watch", "list", "update", "delete"},
				},
			},
		},
	}

	tests := []struct {
		name     string
		current  []*rbacv1.Role
		validate func(t *testing.T, result []*rbacv1.Role)
	}{
		{
			name:    "should merge the roles, and not modify cachedRoles",
			current: []*rbacv1.Role{},
			validate: func(t *testing.T, result []*rbacv1.Role) {
				if len(result) != 2 {
					t.Errorf("expected 2 roles, got %d", len(result))
				}
				if len(result[0].Rules) != 5 {
					t.Errorf("expected 5 rules for merge-test cluster role, got %d", len(result[0].Rules))
				}
				if len(cache) != 3 {
					t.Errorf("expected 3 roles in cache, got %d", len(cache))
				}
				if len(cache[0].Rules) != 2 || len(cache[1].Rules) != 4 {
					t.Errorf("expected rules for merge-test in cache to not be modified, got [0]%d [1]%d", len(cache[0].Rules), len(cache[1].Rules))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shallowCopyAndMergeRoles(tt.current, cache)
			tt.validate(t, result)
		})
	}
}

func Test_copyAndMergeClusterRoles(t *testing.T) {
	cache := []*rbacv1.ClusterRole{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "merge-test",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"list", "update", "delete"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "merge-test",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"update"},
				},
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"create", "get", "watch"},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "other-rule",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"rbac.authorization.k8s.io"},
					Resources: []string{"clusterroles", "clusterrolebindings"},
					Verbs:     []string{"create", "get", "watch", "list", "update", "delete"},
				},
			},
		},
	}

	tests := []struct {
		name     string
		current  []*rbacv1.ClusterRole
		validate func(t *testing.T, result []*rbacv1.ClusterRole)
	}{
		{
			name:    "should merge the roles, and not modify cachedRoles",
			current: []*rbacv1.ClusterRole{},
			validate: func(t *testing.T, result []*rbacv1.ClusterRole) {
				if len(result) != 2 {
					t.Errorf("expected 2 roles, got %d", len(result))
				}
				if len(result[0].Rules) != 5 {
					t.Errorf("expected 5 rules for merge-test cluster role, got %d", len(result[0].Rules))
				}
				if len(cache) != 3 {
					t.Errorf("expected 3 roles in cache, got %d", len(cache))
				}
				if len(cache[0].Rules) != 2 || len(cache[1].Rules) != 4 {
					t.Errorf("expected rules for merge-test in cache to not be modified, got [0]%d [1]%d", len(cache[0].Rules), len(cache[1].Rules))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shallowCopyAndMergeClusterRoles(tt.current, cache)
			tt.validate(t, result)
		})
	}
}
