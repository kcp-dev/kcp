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
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestClaimAuthorizer(t *testing.T) {
	// Default permission claim and request attributes fixtures match
	newPermissionClaimFixture := func() *apisv1alpha1.AcceptablePermissionClaim {
		return &apisv1alpha1.AcceptablePermissionClaim{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Resource: "configmaps"},
				Verbs:         apisv1alpha1.Verbs{RestrictTo: []string{"get"}},
				ResourceSelector: []apisv1alpha1.ResourceSelector{
					{Name: "foo", Namespace: "bar"},
				},
			},
			State: apisv1alpha1.ClaimAccepted,
		}
	}
	newAttributesFixture := func() *authorizer.AttributesRecord {
		return &authorizer.AttributesRecord{
			Verb:      "get",
			Resource:  "configmaps",
			Name:      "foo",
			Namespace: "bar",
		}
	}

	for _, tt := range []struct {
		name         string
		attrFunc     func(*authorizer.AttributesRecord)
		claimFunc    func(*apisv1alpha1.AcceptablePermissionClaim)
		wantDecision authorizer.Decision
		wantReason   string
		wantError    string
	}{
		{
			name:         "match",
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to verb is restricted to in permission claim in API binding name=\"binding\": allowed",
		},
		{
			name: "requested resource differs",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Resource = "DIFFERENT"
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to no API binding bound: allowed",
		},
		{
			name: "requested name differs",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Name = "DIFFERENT"
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to resource doesn't match any resource selector in API binding name=\"binding\": allowed",
		},
		{
			name: "requested verb differs",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "DIFFERENT"
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "verb is not restricted to in API binding name=\"binding\"",
		},
		{
			name: "claimed resource differs",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.GroupResource = apisv1alpha1.GroupResource{Resource: "DIFFERENT"}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to no API binding bound: allowed",
		},
		{
			name: "claimed name differs",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "DIFFERENT", Namespace: "bar"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to resource doesn't match any resource selector in API binding name=\"binding\": allowed",
		},
		{
			name: "claimed namespace differs",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "foo", Namespace: "DIFFERENT"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to resource doesn't match any resource selector in API binding name=\"binding\": allowed",
		},
		{
			name: "claimed name unset",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "bar"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to verb is restricted to in permission claim in API binding name=\"binding\": allowed",
		},
		{
			name: "claimed name unset and requested verb differ",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "bar"},
				}
			},
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "DIFFERENT"
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "verb is not restricted to in API binding name=\"binding\"",
		},
		{
			name: "claimed namespace unset",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "foo", Namespace: ""},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to verb is restricted to in permission claim in API binding name=\"binding\": allowed",
		},
		{
			name: "claimed namespace unset and verb differs",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "foo", Namespace: ""},
				}
			},
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "DIFFERENT"
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "verb is not restricted to in API binding name=\"binding\"",
		},
		{
			name: "claim restricts to wildcard verb",
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.Verbs = apisv1alpha1.Verbs{RestrictTo: []string{"*"}}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to permission claim restricts to \"*\" in API binding name=\"binding\": allowed",
		},
		{
			name: "verb differs while claiming all resources",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "create"
			},
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.All = true
				c.ResourceSelector = nil
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "verb is not restricted to in API binding name=\"binding\"",
		},
		{
			name: "verb differs while claiming namespace matches",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "DIFFERENT"
				a.Name = ""
			},
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "foo"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to resource doesn't match any resource selector in API binding name=\"binding\": allowed",
		},
		{
			name: "verb differs while claiming namespace overlap",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Verb = "DIFFERENT"
				a.Name = ""
				a.Namespace = ""
			},
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "foo"},
				}
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "verb is not restricted to in API binding name=\"binding\"",
		},
		{
			name: "verb matches while claiming namespace overlap",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Name = ""
				a.Namespace = ""
			},
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "foo"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to verb is restricted to in permission claim in API binding name=\"binding\": allowed",
		},
		{
			name: "verb matches wildcard while claiming namespace overlap",
			attrFunc: func(a *authorizer.AttributesRecord) {
				a.Name = ""
				a.Namespace = ""
			},
			claimFunc: func(c *apisv1alpha1.AcceptablePermissionClaim) {
				c.Verbs = apisv1alpha1.Verbs{RestrictTo: []string{"*"}}
				c.ResourceSelector = []apisv1alpha1.ResourceSelector{
					{Name: "", Namespace: "foo"},
				}
			},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "delegating due to permission claim restricts to \"*\" in API binding name=\"binding\": allowed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claim := newPermissionClaimFixture()
			if tt.claimFunc != nil {
				tt.claimFunc(claim)
			}
			attr := newAttributesFixture()
			if tt.attrFunc != nil {
				tt.attrFunc(attr)
			}
			auth := &reversePermissionClaimsAuthorizer{
				getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
					return []*apisv1alpha1.APIBinding{{
						ObjectMeta: v1.ObjectMeta{Name: "binding"},
						Spec: apisv1alpha1.APIBindingSpec{
							PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{*claim},
						},
						Status: apisv1alpha1.APIBindingStatus{},
					}}, nil
				},
				delegate: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
					return authorizer.DecisionAllow, "allowed", nil
				}),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "cluster"})
			dec, reason, err := auth.Authorize(ctx, attr)
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != tt.wantError {
				t.Errorf("want error %q, got %q", tt.wantError, gotErr)
			}
			if dec != tt.wantDecision {
				t.Errorf("want decision %v, got %v", tt.wantDecision, dec)
			}
			if reason != tt.wantReason {
				t.Errorf("want reason %q, got %q", tt.wantReason, reason)
			}
		})
	}
}
