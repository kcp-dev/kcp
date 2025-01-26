/*
Copyright 2025 The KCP Authors.

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

package authorization

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

// TestResolverWithWarrants is a smoke test testing RBAC with kcp extensions:
// - scopes
// - warrants
// - globally valid service accounts.
// Everything of this is already tested in the kube carry patch. But as this
// is important functionality, we want to make sure it is not broken.
func TestResolverWithWarrants(t *testing.T) {
	getPods := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
		APIGroups: []string{""},
		Resources: []string{"pods"},
	}
	getServices := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
		APIGroups: []string{""},
		Resources: []string{"services"},
	}
	getNodes := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
		APIGroups: []string{""},
		Resources: []string{"nodes"},
	}
	getHealthz := &authorizer.DefaultNonResourceRuleInfo{
		Verbs:           []string{"get"},
		NonResourceURLs: []string{"/healthz"},
	}
	getReadyz := &authorizer.DefaultNonResourceRuleInfo{
		Verbs:           []string{"get"},
		NonResourceURLs: []string{"/readyz"},
	}
	getMetrics := &authorizer.DefaultNonResourceRuleInfo{
		Verbs:           []string{"get"},
		NonResourceURLs: []string{"/metrics"},
	}
	getRoot := &authorizer.DefaultNonResourceRuleInfo{
		Verbs:           []string{"get"},
		NonResourceURLs: []string{"/"},
	}

	tests := []struct {
		name                 string
		user                 user.Info
		wantResourceRules    []authorizer.ResourceRuleInfo
		wantNonResourceRules []authorizer.NonResourceRuleInfo
		wantError            bool
	}{
		{
			name:                 "base without warrants",
			user:                 &user.DefaultInfo{Name: "user-a"},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with same warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-a"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with different warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz, getReadyz},
		},
		{
			name:                 "unknown base with warrant",
			user:                 &user.DefaultInfo{Name: "user-c", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
		},
		{
			name:                 "base with unknown warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-c"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with multiple warrants",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b"}`, `{"user":"user-b"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz, getReadyz},
		},
		{
			name:                 "base with invalid warrant, ignored",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`invalid`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "service account without cluster",
			user:                 &user.DefaultInfo{Name: "system:serviceaccount:default:sa", Groups: []string{"system:serviceaccounts", user.AllAuthenticated}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getServices},
			wantNonResourceRules: nil, // global service accounts do no work without a cluster.
		},
		{
			name:                 "service account with this cluster",
			user:                 &user.DefaultInfo{Name: "system:serviceaccount:default:sa", Groups: []string{"system:serviceaccounts", user.AllAuthenticated}, Extra: map[string][]string{authserviceaccount.ClusterNameKey: {"this"}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getReadyz},
		},
		{
			name:                 "service account with other cluster",
			user:                 &user.DefaultInfo{Name: "system:serviceaccount:default:sa", Groups: []string{"system:serviceaccounts", user.AllAuthenticated}, Extra: map[string][]string{authserviceaccount.ClusterNameKey: {"other"}}},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getMetrics},
		},
		{
			name:                 "base with service account warrant without cluster, ignored",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:sa"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with service account warrant with this cluster",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:sa","extra":{"authentication.kcp.io/scopes": ["cluster:this"]}}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with service account warrant with other cluster",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:sa","extra":{"authentication.kcp.io/scopes": ["cluster:other"]}}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "base with out of scope warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b", "extra":{"authentication.kcp.io/scopes": ["cluster:another"]}}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "out of scope base with warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b"}`}, rbacregistryvalidation.ScopeExtraKey: {"cluster:another"}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
		},
		{
			name:                 "base with foreign service account warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:foo","extra":{"authentication.kubernetes.io/cluster-name": ["another"]}}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
		},
		{
			name:                 "foreign service account with warrant",
			user:                 &user.DefaultInfo{Name: "system:serviceaccount:default:foo", Extra: map[string][]string{rbacregistryvalidation.WarrantExtraKey: {`{"user":"user-b"}`}, authserviceaccount.ClusterNameKey: {"another"}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, sr := rbacregistryvalidation.NewTestRuleResolver(nil, nil,
				[]*rbacv1.ClusterRole{{
					ObjectMeta: metav1.ObjectMeta{Name: "get-pods"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-nodes"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"nodes"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-services"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"services"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-healthz"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"/healthz"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-readyz"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"/readyz"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-metrics"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"/metrics"}}},
				}, {
					ObjectMeta: metav1.ObjectMeta{Name: "get-root"},
					Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, NonResourceURLs: []string{"/"}}},
				}},
				[]*rbacv1.ClusterRoleBinding{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-pods"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-a"},
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-b"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-pods"},
					}, {
						ObjectMeta: metav1.ObjectMeta{Name: "get-nodes"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-a"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-nodes"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-services"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-b"},
							{Kind: "ServiceAccount", APIGroup: "", Namespace: "default", Name: "sa"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-services"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-healthz"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-a"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-healthz"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-readyz"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-b"},
							{Kind: "User", APIGroup: "", Name: "system:kcp:serviceaccount:this:default:sa"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-readyz"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-metrics"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "", Name: "system:kcp:serviceaccount:other:default:sa"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-metrics"},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "get-root"},
						Subjects: []rbacv1.Subject{
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-a"},
							{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "user-b"},
						},
						RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", APIGroup: "rbac.authorization.k8s.io", Name: "get-root"},
					},
				})
			resolver := rbac.New(sr, sr, sr, sr)

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "this"})
			resourceRules, nonResourceRules, _, err := resolver.RulesFor(ctx, tt.user, "")

			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}

			sort.Sort(sortedResourceRules(tt.wantResourceRules))
			sort.Sort(sortedNonResourceRules(tt.wantNonResourceRules))

			sort.Sort(sortedResourceRules(resourceRules))
			sort.Sort(sortedNonResourceRules(nonResourceRules))

			if !tt.wantError {
				if diff := cmp.Diff(resourceRules, tt.wantResourceRules); diff != "" {
					t.Errorf("resourceRules differs: +want -got:\n%s", diff)
				}
				if diff := cmp.Diff(nonResourceRules, tt.wantNonResourceRules); diff != "" {
					t.Errorf("nonResourceRules differs: +want -got:\n%s", diff)
				}
			}
		})
	}
}

type sortedResourceRules []authorizer.ResourceRuleInfo

func (s sortedResourceRules) Len() int {
	return len(s)
}

func (s sortedResourceRules) Less(i, j int) bool {
	return fmt.Sprintf("%#v", s[i]) < fmt.Sprintf("%#v", s[j])
}

func (s sortedResourceRules) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

type sortedNonResourceRules []authorizer.NonResourceRuleInfo

func (s sortedNonResourceRules) Len() int {
	return len(s)
}

func (s sortedNonResourceRules) Less(i, j int) bool {
	return fmt.Sprintf("%#v", s[i]) < fmt.Sprintf("%#v", s[j])
}

func (s sortedNonResourceRules) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
