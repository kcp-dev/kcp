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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"

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
				validation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster},
			},
			wantAllowed: true,
		},
		{
			name:   "in-scope users multiple scopes",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				validation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster + ",cluster:another"},
			},
			wantAllowed: true,
		},
		{
			name:   "in-scope users conflicting scopes",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				validation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster, "cluster:another"},
			},
			wantAllowed: false,
		},
		{
			name:   "out-of-scoped users",
			user:   "user",
			groups: []string{"system:kcp:admin"},
			extra: map[string]authorizationv1.ExtraValue{
				validation.ScopeExtraKey: {"cluster:root"},
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
				serviceaccount.ClusterNameKey: {"root"},
			},
			wantAllowed: false,
		},
		{name: "service account with other cluster and warrant", user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string]authorizationv1.ExtraValue{
			serviceaccount.ClusterNameKey: {"root"},
			validation.WarrantExtraKey:    {`{"user":"user","groups":["system:kcp:admin"]}`},
		}, wantAllowed: true},
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

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	wsPath, ws := framework.NewOrganizationFixture(t, server)

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
			user: "user", groups: []string{"system:kcp:admin"}, extra: map[string][]string{validation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster}},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
		{
			name: "out-of-scoped users",
			user: "user", groups: []string{"system:kcp:admin"}, extra: map[string][]string{validation.ScopeExtraKey: {"cluster:root"}},
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
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{serviceaccount.ClusterNameKey: {"root"}},
			wantRules: authenticatedBaseRules,
		},
		{
			name: "service account with other cluster and warrant",
			user: "system:serviceaccount:default:default", groups: []string{"system:kcp:admin"}, extra: map[string][]string{
				serviceaccount.ClusterNameKey: {"root"},
				validation.WarrantExtraKey:    {`{"user":"user","groups":["system:kcp:admin"]}`},
			},
			wantRules: append([]authorizationv1.ResourceRule{
				{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
			}, authenticatedBaseRules...),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// impersonate as user, using the cfg as the base, adding a warrant for basic workspace access
			impersonationConfig := rest.CopyConfig(cfg)
			impersonationConfig.Impersonate = rest.ImpersonationConfig{
				UserName: tt.user,
				Groups:   tt.groups,
				Extra:    tt.extra,
			}
			if impersonationConfig.Impersonate.Extra == nil {
				impersonationConfig.Impersonate.Extra = map[string][]string{}
			}
			impersonationConfig.Impersonate.Extra[validation.WarrantExtraKey] = append(impersonationConfig.Impersonate.Extra[validation.WarrantExtraKey],
				fmt.Sprintf(`{"user":"system:serviceaccount:default:default","groups":["system:authenticated"],"extra":{"authentication.kubernetes.io/cluster-name":[%q]}}`, ws.Spec.Cluster))
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
