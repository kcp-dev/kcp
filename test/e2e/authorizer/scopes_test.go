/*
Copyright 2024 The KCP Authors.

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

package authorizer

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSubjectAccessReview(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	wsPath, ws := framework.NewOrganizationFixture(t, server)

	clusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	type tests struct {
		name        string
		user        string
		groups      []string
		extra       map[string]authorizationv1.ExtraValue
		wantAllowed bool
	}
	for _, tt := range []tests{
		{
			name:        "normal user",
			user:        "user",
			groups:      []string{"system:kcp:admin"},
			wantAllowed: true,
		},
		{
			name:   "in-scope users",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				rbacregistryvalidation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster},
			},
			wantAllowed: true,
		},
		{
			name:   "in-scope users multiple scopes",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				rbacregistryvalidation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster + ",cluster:another"},
			},
			wantAllowed: true,
		},
		{
			name:   "in-scope users conflicting scopes",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				rbacregistryvalidation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster, "cluster:another"},
			},
			wantAllowed: false,
		},
		{
			name:   "out-of-scoped users",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				rbacregistryvalidation.ScopeExtraKey: {"cluster:root"},
			},
			wantAllowed: false,
		},
		{
			name:        "service account",
			user:        "system:serviceaccount:default:default",
			groups:      []string{"system:kcp:admin"},
			wantAllowed: true,
		},
		{
			name:   "service account with cluster",
			user:   "system:serviceaccount:default:default",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				serviceaccount.ClusterNameKey: {ws.Spec.Cluster},
			},
			wantAllowed: true,
		},
		{
			name:   "service account with other cluster",
			user:   "system:serviceaccount:default:default",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				serviceaccount.ClusterNameKey: {"other"},
			},
			wantAllowed: false,
		},
		{
			name: "service account with other cluster and warrant",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string]authorizationv1.ExtraValue{
				serviceaccount.ClusterNameKey:          {"other"},
				rbacregistryvalidation.WarrantExtraKey: {`{"user":"user","groups":["system:kcp:admin"]}`},
			},
			wantAllowed: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			req := &authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Verb:     "get",
						Resource: "namespaces",
						Version:  "v1",
					},
					User:   tt.user,
					Groups: tt.groups,
					Extra:  tt.extra,
				},
			}
			resp, err := clusterClient.Cluster(wsPath).AuthorizationV1().SubjectAccessReviews().Create(ctx, req, metav1.CreateOptions{})
			require.NoError(t, err)
			if tt.wantAllowed {
				require.True(t, resp.Status.Allowed, "expected allowed, got: %s", resp.Status.Reason)
			} else {
				require.False(t, resp.Status.Allowed, "expected denied, got: %s", resp.Status.Reason)
			}
		})
	}
}

func TestSelfSubjectRulesReview(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	wsPath, ws := framework.NewOrganizationFixture(t, server)

	t.Log("User scoped to a another workspace has no access in the beginning")
	foreignConfig := rest.CopyConfig(cfg)
	foreignConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "user",
		Groups:   []string{user.AllAuthenticated},
		Extra:    map[string][]string{rbacregistryvalidation.ScopeExtraKey: {"cluster:other"}}}
	foreignClusterClient, err := kcpkubernetesclientset.NewForConfig(foreignConfig)
	require.NoError(t, err)
	req := &authorizationv1.SelfSubjectRulesReview{Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: "default"}}
	_, err = foreignClusterClient.Cluster(wsPath).AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, req, metav1.CreateOptions{})
	require.Error(t, err)

	t.Log("Give everybody authenticated access to the workspace")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-access"},
		Rules:      []rbacv1.PolicyRule{{Verbs: []string{"access"}, NonResourceURLs: []string{"/"}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create cluster role")
	_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "sa-access"},
		Subjects:   []rbacv1.Subject{{Kind: "Group", APIGroup: "rbac.authorization.k8s.io", Name: "system:authenticated"}},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "sa-access", APIGroup: rbacv1.GroupName},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create cluster role binding")

	t.Log("Wait until the cluster role binding is effective, i.e. the foreign use can access too")
	require.NoError(t, err)
	framework.Eventually(t, func() (bool, string) {
		req := &authorizationv1.SelfSubjectRulesReview{Spec: authorizationv1.SelfSubjectRulesReviewSpec{Namespace: "default"}}
		_, err := foreignClusterClient.Cluster(wsPath).AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, req, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// These rules will always exist as soon as a user has access
	authenticatedBaseRules := []authorizationv1.ResourceRule{
		{Verbs: []string{"create"}, APIGroups: []string{"authentication.k8s.io"}, Resources: []string{"selfsubjectreviews"}},
		{Verbs: []string{"create"}, APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"}},
	}

	type tests struct {
		name      string
		user      string
		groups    []string
		extra     map[string][]string
		wantRules []authorizationv1.ResourceRule
	}
	for _, tt := range []tests{
		{
			name: "normal user",
			user: "user", groups: []string{"system:kcp:admin"},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
		{
			name: "in-scope users",
			user: "user", groups: []string{"system:kcp:admin"}, extra: map[string][]string{rbacregistryvalidation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster}},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
		{
			name: "out-of-scoped users",
			user: "user", groups: []string{"system:kcp:admin"}, extra: map[string][]string{rbacregistryvalidation.ScopeExtraKey: {"cluster:other"}},
			wantRules: authenticatedBaseRules,
		},
		{
			name: "service account",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
		{
			name: "service account with cluster",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{serviceaccount.ClusterNameKey: {ws.Spec.Cluster}},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
		{
			name: "service account with other cluster",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{serviceaccount.ClusterNameKey: {"other"}},
			wantRules: authenticatedBaseRules,
		},
		{
			name: "admin scoped to other cluster",
			user: "user", groups: []string{"system:kcp:admin", user.AllAuthenticated}, extra: map[string][]string{rbacregistryvalidation.ScopeExtraKey: {"cluster:other"}},
			wantRules: authenticatedBaseRules,
		},
		{
			name: "service account with scope to other cluster",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{
				serviceaccount.ClusterNameKey:        {ws.Spec.Cluster},
				rbacregistryvalidation.ScopeExtraKey: {"cluster:other"},
			},
			wantRules: authenticatedBaseRules,
		},
		{
			name: "service account with other cluster and warrant",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{
				serviceaccount.ClusterNameKey:          {"other"},
				rbacregistryvalidation.WarrantExtraKey: {`{"user":"user","groups":["system:kcp:admin"]}`},
			},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// impersonate as user, using the cfg as the base.
			impersonationConfig := rest.CopyConfig(cfg)
			impersonationConfig.Impersonate = rest.ImpersonationConfig{
				UserName: tt.user,
				Groups:   tt.groups,
				Extra:    tt.extra,
			}
			if impersonationConfig.Impersonate.Extra == nil {
				impersonationConfig.Impersonate.Extra = map[string][]string{}
			}
			impersonatedClient, err := kcpkubernetesclientset.NewForConfig(impersonationConfig)
			require.NoError(t, err)

			req := &authorizationv1.SelfSubjectRulesReview{
				Spec: authorizationv1.SelfSubjectRulesReviewSpec{
					Namespace: "default",
				},
			}
			resp, err := impersonatedClient.Cluster(wsPath).AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, req, metav1.CreateOptions{})
			require.NoError(t, err)

			sort.Sort(sortedResourceRules(resp.Status.ResourceRules))
			sort.Sort(sortedResourceRules(tt.wantRules))
			require.Equal(t, tt.wantRules, resp.Status.ResourceRules)
		})
	}
}

type sortedResourceRules []authorizationv1.ResourceRule

func (r sortedResourceRules) Len() int           { return len(r) }
func (r sortedResourceRules) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
func (r sortedResourceRules) Less(i, j int) bool { return r[i].String() < r[j].String() }
