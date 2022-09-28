/*
Copyright 2022 The KCP Authors.

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

	"k8s.io/apiserver/pkg/authorization/authorizer"
)

func TestIntersectionAuthorizer(t *testing.T) {
	newAuthorizer := func(decision authorizer.Decision, err error) authorizer.Authorizer {
		return authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
			return decision, "", err
		})
	}
	_ = newAuthorizer

	for name, tc := range map[string]struct {
		authorizers  []authorizer.Authorizer
		wantDecision authorizer.Decision
		wantErr      string
	}{
		"empty": {
			wantDecision: authorizer.DecisionNoOpinion,
		},
		"all allow": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionAllow, nil),
			},
			wantDecision: authorizer.DecisionAllow,
		},
		"all deny": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionDeny, nil),
				newAuthorizer(authorizer.DecisionDeny, nil),
			},
			wantDecision: authorizer.DecisionDeny,
		},
		"all have no opinion": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
			},
			wantDecision: authorizer.DecisionNoOpinion,
		},
		"one allows, one has no opinion": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
			},
			wantDecision: authorizer.DecisionAllow,
		},
		"one denies, one has no opinion": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionDeny, nil),
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
			},
			wantDecision: authorizer.DecisionDeny,
		},
		"one allows, one denies": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionDeny, nil),
			},
			wantDecision: authorizer.DecisionDeny,
		},
		"all differ": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionDeny, nil),
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
			},
			wantDecision: authorizer.DecisionDeny,
		},
		"one authorizer has errors but one denies": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionDeny, nil),
				newAuthorizer(authorizer.DecisionNoOpinion, nil),
				newAuthorizer(authorizer.DecisionAllow, errors.New("boom")),
			},
			wantDecision: authorizer.DecisionDeny,
		},
		"one authorizer has errors": {
			authorizers: []authorizer.Authorizer{
				newAuthorizer(authorizer.DecisionAllow, nil),
				newAuthorizer(authorizer.DecisionAllow, errors.New("boom")),
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantErr:      "boom",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dec, _, err := IntersectionAuthorizer(tc.authorizers).Authorize(context.Background(), authorizer.AttributesRecord{})
			if got := dec; got != tc.wantDecision {
				t.Errorf("want decision %v got %v", tc.wantDecision, got)
			}
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tc.wantErr {
				t.Errorf("want error %q got %q", tc.wantErr, gotErr)
			}
		})
	}
}
