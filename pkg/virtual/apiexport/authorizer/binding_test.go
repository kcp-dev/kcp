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

package authorizer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

const (
	apidomainKey = "foo/bar"
)

var (
	defaultAPIExport = &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bar",
		},
	}
)

func TestBoundAPIAuthorizer(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		attr                  authorizer.Attributes
		apidomainKey          string
		getAPIExport          func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error)
		getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error)

		expectedErr      string
		expectedDecision authorizer.Decision
		expectedReason   string
	}{
		{
			name:             "invalid domain key",
			attr:             &authorizer.AttributesRecord{User: &user.DefaultInfo{}},
			apidomainKey:     "",
			expectedDecision: authorizer.DecisionNoOpinion,
			expectedErr:      "error getting valid cluster from context: cluster path is empty in the request context",
		},
		{
			name: "list request to the bound API",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Status: apisv1alpha2.APIBindingStatus{
						BoundResources: []apisv1alpha2.BoundAPIResource{
							{
								Group:    "foo",
								Resource: "bar",
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return defaultAPIExport, nil
			},
			expectedDecision: authorizer.DecisionAllow,
		},
		{
			name: "list request to a non-bound API",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "baz",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Status: apisv1alpha2.APIBindingStatus{
						BoundResources: []apisv1alpha2.BoundAPIResource{
							{
								Group:    "foo",
								Resource: "bar",
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return defaultAPIExport, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "list request to the API bound via permission claims",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"list"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"list"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionAllow,
		},
		{
			name: "list request to the API bound via permission claims allowing wildcard",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"*"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"*"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionAllow,
		},
		{
			name: "list request to the API bound via permission claims allowing wildcard (rejected claim)",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"*"},
								},
								State: apisv1alpha2.ClaimRejected,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"*"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "list request to the API bound via permission claims (rejected claim)",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"list"},
								},
								State: apisv1alpha2.ClaimRejected,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"list"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "list request when both APIExport and APIBinding do not allow list",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"get"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"get"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "list request when APIExport allows list but APIBinding does not",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"get"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"get", "list"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "list request when APIBinding allows list but APIExport does not",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "list",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"get", "list"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"get"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "failed to find suitable reason to allow access in APIBinding",
		},
		{
			name: "non-standard verb (connect)",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "foo",
				Resource: "bar",
				Verb:     "connect",
			},
			apidomainKey: apidomainKey,
			getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha2.APIBinding, error) {
				return &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "foo",
										Resource: "bar",
									},
									All:   true,
									Verbs: []string{"connect", "proxy"},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				}, nil
			},
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "foo",
									Resource: "bar",
								},
								All:   true,
								Verbs: []string{"connect", "proxy"},
							},
						},
					},
				}, nil
			},
			expectedDecision: authorizer.DecisionAllow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc, _ := logicalcluster.NewPath(tc.apidomainKey).Name()
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: lc})
			ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(tc.apidomainKey))
			auth := &boundAPIAuthorizer{
				getAPIExport:          tc.getAPIExport,
				getAPIBindingByExport: tc.getAPIBindingByExport,
				delegate: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
					return tc.expectedDecision, "", nil
				}),
			}
			dec, reason, err := auth.Authorize(ctx, tc.attr)
			errString := ""
			if err != nil {
				errString = err.Error()
			}
			require.Equal(t, errString, tc.expectedErr)
			require.Equal(t, tc.expectedDecision, dec)
			require.Equal(t, tc.expectedReason, reason)
		})
	}
}
