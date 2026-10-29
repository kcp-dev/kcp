/*
Copyright 2026 The kcp Authors.

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
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestLogicalClusterManagementBypassAuthorizer(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		groups       []string
		deepSAR      bool
		policyResult authorizer.Decision
		policyErr    error
		wantDecision authorizer.Decision
		wantReason   string
		wantErr      bool
	}{
		"bootstrap policy allows the request": {
			groups:       []string{bootstrap.SystemLogicalClusterAdmin},
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "logical cluster management: allowed",
		},
		"external logical cluster admin is admitted the same way": {
			groups:       []string{bootstrap.SystemExternalLogicalClusterAdmin},
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "logical cluster management: allowed",
		},
		// Locks in that the bypass is bounded by the bootstrap policy: anything those
		// identities are not meant to do goes through the regular chain.
		"request outside the bootstrap policy falls through": {
			groups:       []string{bootstrap.SystemLogicalClusterAdmin},
			policyResult: authorizer.DecisionNoOpinion,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not covered by the bootstrap policy",
		},
		"other identities are not bypassed": {
			groups:       []string{"system:kcp:admin"},
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not a logical cluster management identity",
		},
		"deep SAR is skipped": {
			groups:       []string{bootstrap.SystemLogicalClusterAdmin},
			deepSAR:      true,
			policyResult: authorizer.DecisionAllow,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"bootstrap policy error": {
			groups:       []string{bootstrap.SystemLogicalClusterAdmin},
			policyErr:    fmt.Errorf("informer not synced"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "error checking bootstrap policy",
			wantErr:      true,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			a := NewLogicalClusterManagementBypassAuthorizer(
				authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
					if cas.policyErr != nil {
						return authorizer.DecisionNoOpinion, "policy error", cas.policyErr
					}
					return cas.policyResult, "allowed", nil
				}),
			)

			attr := authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "lcluster-admin", Groups: cas.groups}}

			decision, reason, err := a.Authorize(ctx, attr)
			if cas.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
