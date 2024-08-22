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

package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"
)

func TestWithWarrants(t *testing.T) {
	tests := []struct {
		name       string
		user       user.Info
		wantDec    authorizer.Decision
		wantReason string
		wantError  bool
	}{
		{
			name:       "base without warrants",
			user:       &user.DefaultInfo{Name: "user-access"},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-access",
		},
		{
			name:       "base without warrants deny",
			user:       &user.DefaultInfo{Name: "user-deny"},
			wantDec:    authorizer.DecisionDeny,
			wantReason: "user-deny",
		},
		{
			name:      "base without warrants error",
			user:      &user.DefaultInfo{Name: "user-error"},
			wantDec:   authorizer.DecisionNoOpinion,
			wantError: true,
		},
		{
			name:       "base without warrants user-unknown",
			user:       &user.DefaultInfo{Name: "unknown"},
			wantDec:    authorizer.DecisionNoOpinion,
			wantReason: "user-unknown",
		},
		{
			name:       "allowed base with allowed warrants",
			user:       &user.DefaultInfo{Name: "user-access", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-access",
		},
		{
			name:       "denied base with allowed warrants",
			user:       &user.DefaultInfo{Name: "user-deny", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}}},
			wantDec:    authorizer.DecisionDeny,
			wantReason: "user-deny",
		},
		{
			name:      "error base with allowed warrants",
			user:      &user.DefaultInfo{Name: "user-error", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}}},
			wantDec:   authorizer.DecisionNoOpinion,
			wantError: true,
		},
		{
			name:       "no opinion base with allowed warrants",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-unknown; warrants: [user-access]",
		},
		{
			name:       "allowed base with denied warrants",
			user:       &user.DefaultInfo{Name: "user-access", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-deny"}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-access",
		},
		{
			name:       "no opinion base with denied warrants",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-deny"}`}}},
			wantDec:    authorizer.DecisionNoOpinion,
			wantReason: "user-unknown; warrants: [user-deny]",
		},
		{
			name:       "denied base with denied warrants",
			user:       &user.DefaultInfo{Name: "user-deny", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-deny"}`}}},
			wantDec:    authorizer.DecisionDeny,
			wantReason: "user-deny",
		},
		{
			name:       "no opinion base with invalid warrant",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`invalid`}}},
			wantDec:    authorizer.DecisionNoOpinion,
			wantReason: "user-unknown; warrants: [failed to unmarshal warrant: invalid character 'i' looking for beginning of value]",
		},
		{
			name:       "no opinion base with invalid warrant and valid warrant",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`invalid`, `{"user":"user-access"}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-unknown; warrants: [failed to unmarshal warrant: invalid character 'i' looking for beginning of value; user-access]",
		},
		{
			name:       "no opinion base with multiple warrants, deny and allowed",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-deny"}`, `{"user":"user-access"}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-unknown; warrants: [user-deny; user-access]",
		},
		{
			name:       "nested warrants",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"unknown", "extra": {"authorization.kcp.io/warrant": ["{\"user\":\"user-access\"}"]}}`}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "user-unknown; warrants: [user-unknown; warrants: [user-access]]",
		},
		{
			name:       "nested warrants with deny in the middle",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-deny", "extra": {"authorization.kcp.io/warrant": ["{\"user\":\"user-access\"}"]}}`}}},
			wantDec:    authorizer.DecisionNoOpinion,
			wantReason: "user-unknown; warrants: [user-deny]",
		},
		{
			name:       "out of scope user with warrants",
			user:       &user.DefaultInfo{Name: "user-access", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}, validation.ScopeExtraKey: {"cluster:another"}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "out-of-scope user; warrants: [user-access]",
		},
		{
			name:       "foreigh service account with warrants",
			user:       &user.DefaultInfo{Name: "system:serviceaccount:default:foo", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-access"}`}, authserviceaccount.ClusterNameKey: {"another"}}},
			wantDec:    authorizer.DecisionAllow,
			wantReason: "foreign service account; warrants: [user-access]",
		},
		{
			name:       "no opinion base with a warrant serviceaccount that lacks the scope",
			user:       &user.DefaultInfo{Name: "unknown", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:foo"}`}}},
			wantDec:    authorizer.DecisionNoOpinion,
			wantReason: "user-unknown; warrants: [skipping warrant for serviceaccount without cluster scope]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				switch a.GetUser().GetName() {
				case "user-access":
					return authorizer.DecisionAllow, "user-access", nil
				case "user-deny":
					return authorizer.DecisionDeny, "user-deny", nil
				case "user-error":
					return authorizer.DecisionNoOpinion, "", errors.New("some error")
				default:
					return authorizer.DecisionNoOpinion, "user-unknown", nil
				}
			})
			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "this"})

			dec, reason, err := WithWarrants(base).Authorize(ctx, &authorizer.AttributesRecord{User: tt.user})

			if err != nil && !tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && tt.wantError {
				t.Errorf("expected error, got none")
			}
			if dec != tt.wantDec {
				t.Errorf("dec = %v, want %v", dec, tt.wantDec)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

func TestResolverWithWarrants(t *testing.T) {
	getPods := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
		Resources: []string{"pods"},
	}
	getServices := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
		Resources: []string{"services"},
	}
	getNodes := &authorizer.DefaultResourceRuleInfo{
		Verbs:     []string{"get"},
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
	getRoot := &authorizer.DefaultNonResourceRuleInfo{
		Verbs:           []string{"get"},
		NonResourceURLs: []string{"/"},
	}

	tests := []struct {
		name                 string
		user                 user.Info
		namespace            string
		wantResourceRules    []authorizer.ResourceRuleInfo
		wantNonResourceRules []authorizer.NonResourceRuleInfo
		wantIncomplete       bool
		wantError            bool
	}{
		{
			name:                 "base without warrants",
			user:                 &user.DefaultInfo{Name: "user-a"},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       false,
		},
		{
			name:                 "base with same warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-a"}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       false,
		},
		{
			name:                 "base with different warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b"}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz, getReadyz},
			wantIncomplete:       false,
		},
		{
			name:                 "unknown base with warrant",
			user:                 &user.DefaultInfo{Name: "user-c", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b"}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
			wantIncomplete:       false,
		},
		{
			name:                 "base with unknown warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-c"}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       false,
		},
		{
			name:                 "base with multiple warrants",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b"}`, `{"user":"user-b"}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz, getReadyz},
			wantIncomplete:       false,
		},
		{
			name:      "base with invalid warrant",
			user:      &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`invalid`}}},
			namespace: "default",
			wantError: true,
		},
		{
			name:      "base with service account warrant without cluster scope",
			user:      &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:foo"}`}}},
			namespace: "default",
			wantError: true,
		},
		{
			name:                 "base with out of scope warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b", "extra":{"authentication.kcp.io/scopes": ["cluster:another"]}}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       false,
		},
		{
			name:                 "out of scope base with warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b"}`}, validation.ScopeExtraKey: {"cluster:another"}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
			wantIncomplete:       false,
		},
		{
			name:                 "base with foreign service account warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:foo","extra":{"authentication.kubernetes.io/cluster-name": ["another"]}}`}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       false,
		},
		{
			name:                 "foreign service account with warrant",
			user:                 &user.DefaultInfo{Name: "system:serviceaccount:default:foo", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-b"}`}, authserviceaccount.ClusterNameKey: {"another"}}},
			namespace:            "default",
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getReadyz},
			wantIncomplete:       false,
		},
		{
			name:                 "incomplete base with warrant",
			user:                 &user.DefaultInfo{Name: "user-incomplete", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-a"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getServices, getPods, getNodes},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       true,
		},
		{
			name:                 "base with incomplete warrant",
			user:                 &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-incomplete"}`}}},
			wantResourceRules:    []authorizer.ResourceRuleInfo{getPods, getNodes, getServices},
			wantNonResourceRules: []authorizer.NonResourceRuleInfo{getRoot, getHealthz},
			wantIncomplete:       true,
		},
		{
			name:      "base with error warrant",
			user:      &user.DefaultInfo{Name: "user-a", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-error"}`}}},
			wantError: true,
		},
		{
			name:      "error base with warrant",
			user:      &user.DefaultInfo{Name: "user-error", Extra: map[string][]string{validation.WarrantExtraKey: {`{"user":"user-a"}`}}},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := RuleResolverFunc(func(ctx context.Context, u user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
				switch u.GetName() {
				case "user-a":
					return []authorizer.ResourceRuleInfo{getPods, getNodes}, []authorizer.NonResourceRuleInfo{getRoot, getHealthz}, false, nil
				case "user-b":
					return []authorizer.ResourceRuleInfo{getPods, getServices}, []authorizer.NonResourceRuleInfo{getRoot, getReadyz}, false, nil
				case "user-incomplete":
					return []authorizer.ResourceRuleInfo{getServices}, []authorizer.NonResourceRuleInfo{}, true, nil
				case "user-error":
					return nil, nil, false, errors.New("user-error")
				default:
					return nil, nil, false, nil
				}
			})

			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "this"})
			recursive := ResolverWithWarrants(base)
			resourceRules, nonResourceRules, incomplete, err := recursive.RulesFor(ctx, tt.user, tt.namespace)

			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantError {
				if diff := cmp.Diff(resourceRules, tt.wantResourceRules); diff != "" {
					t.Errorf("resourceRules differs: +want -got:\n%s", diff)
				}
				if diff := cmp.Diff(nonResourceRules, tt.wantNonResourceRules); diff != "" {
					t.Errorf("nonResourceRules differs: +want -got:\n%s", diff)
				}
				if incomplete != tt.wantIncomplete {
					t.Errorf("incomplete = %v, want %v", incomplete, tt.wantIncomplete)
				}
			}
		})
	}
}
