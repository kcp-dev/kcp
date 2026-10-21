/*
Copyright 2022 The kcp Authors.

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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

func TestMaximalPermissionPolicyAuthorizer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                    string
		attr                    authorizer.Attributes
		apidomainKey            string
		getAPIExport            func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error)
		getAPIExportsByIdentity func(identityHash string) ([]*apisv1alpha2.APIExport, error)
		newDeepSARAuthorizer    func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)
		resolveIdentities       func(export *apisv1alpha2.APIExport, cluster logicalcluster.Name, wildcard bool, gr schema.GroupResource) ([]string, error)
		isBuiltIn               func(gr apis.GroupResource) bool
		cluster                 *genericapirequest.Cluster

		expectedErr      string
		expectedDecision authorizer.Decision
		expectedReason   string
	}{
		{
			name:             "invalid domain key",
			attr:             &authorizer.AttributesRecord{User: &user.DefaultInfo{}},
			apidomainKey:     "",
			expectedDecision: authorizer.DecisionNoOpinion,
			expectedErr:      "invalid API domain key",
		},
		{
			name:         "no claimed identities",
			attr:         &authorizer.AttributesRecord{User: &user.DefaultInfo{}},
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
				}, nil
			},

			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   `unclaimed resource in API export: "fooExport", workspace :"someWorkspace"`,
		},
		{
			// A built-in resource (configmaps) carries no identity by
			// construction, so no maximal permission policy can exist for it.
			name: "claimed built-in resource without identity hash",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			isBuiltIn:    func(apis.GroupResource) bool { return true },
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "someGroup",
									Resource: "someResource",
								},
							},
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "claimedGroup",
									Resource: "claimedResource",
								},
							},
						},
					},
				}, nil
			},

			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   `unclaimable resource, identity hash not set in claiming API export: "fooExport", workspace :"someWorkspace"`,
		},
		{
			name: "claimed identity without api export",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "someGroup",
									Resource: "someResource",
								},
							},
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "claimedGroup",
									Resource: "claimedResource",
								},
								IdentityHash: "123",
							},
						},
					},
				}, nil
			},
			getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
				return []*apisv1alpha2.APIExport{}, nil
			},

			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   `no API export providing claimed resources found for identity hash: "123"`,
		},
		{
			name: "claimed identity with api export having no maximum permission policy",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "someGroup",
									Resource: "someResource",
								},
							},
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "claimedGroup",
									Resource: "claimedResource",
								},
								IdentityHash: "123",
							},
						},
					},
				}, nil
			},
			getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
				return []*apisv1alpha2.APIExport{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
					},
				}, nil
			},

			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   `all claimed API exports granted access`,
		},
		{
			name: "claimed identity with api export having maximum permission policy granting access",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "someGroup",
									Resource: "someResource",
								},
							},
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "claimedGroup",
									Resource: "claimedResource",
								},
								IdentityHash: "123",
							},
						},
					},
				}, nil
			},
			getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
				return []*apisv1alpha2.APIExport{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Spec: apisv1alpha2.APIExportSpec{
							MaximalPermissionPolicy: &apisv1alpha2.MaximalPermissionPolicy{Local: &apisv1alpha2.LocalAPIExportPolicy{}},
						},
					},
				}, nil
			},
			newDeepSARAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
				return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
					return authorizer.DecisionAllow, "", nil
				}), nil
			},

			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   `all claimed API exports granted access`,
		},
		{
			name: "claimed identity with api export having maximum permission policy denying access",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
				return &apisv1alpha2.APIExport{
					ObjectMeta: metav1.ObjectMeta{
						Name: "fooExport",
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "someWorkspace",
						},
					},
					Spec: apisv1alpha2.APIExportSpec{
						PermissionClaims: []apisv1alpha2.PermissionClaim{
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "someGroup",
									Resource: "someResource",
								},
							},
							{
								GroupResource: apisv1alpha2.GroupResource{
									Group:    "claimedGroup",
									Resource: "claimedResource",
								},
								IdentityHash: "123",
							},
						},
					},
				}, nil
			},
			getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
				return []*apisv1alpha2.APIExport{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "fooExport",
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "someWorkspace",
							},
						},
						Spec: apisv1alpha2.APIExportSpec{
							MaximalPermissionPolicy: &apisv1alpha2.MaximalPermissionPolicy{Local: &apisv1alpha2.LocalAPIExportPolicy{}},
						},
					},
				}, nil
			},
			newDeepSARAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
				return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
					return authorizer.DecisionDeny, "access denied", nil
				}), nil
			},

			expectedDecision: authorizer.DecisionNoOpinion,
			expectedReason:   `API export: "fooExport", workspace: "someWorkspace" RBAC decision: access denied`,
		},
		{
			// An identity-agnostic claim resolves to the identity the consumer
			// workspace bound, and that producer's policy is enforced.
			name: "identity-agnostic claim resolves to the consumer's bound identity",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			cluster:      &genericapirequest.Cluster{Name: "consumerWorkspace"},
			getAPIExport: identityAgnosticClaimingExport,
			resolveIdentities: func(_ *apisv1alpha2.APIExport, cluster logicalcluster.Name, wildcard bool, _ schema.GroupResource) ([]string, error) {
				if wildcard || cluster != "consumerWorkspace" {
					return nil, nil
				}
				return []string{"resolved-identity"}, nil
			},
			getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
				if identityHash != "resolved-identity" {
					return nil, nil
				}
				return []*apisv1alpha2.APIExport{providerExportWithLocalPolicy()}, nil
			},
			newDeepSARAuthorizer: func(logicalcluster.Name) (authorizer.Authorizer, error) {
				return authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
					return authorizer.DecisionAllow, "", nil
				}), nil
			},

			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   "all claimed API exports granted access",
		},
		{
			// Nothing in the consumer workspace serves the claimed resource, so
			// there is no policy to check against. Deny rather than let the
			// request through unchecked.
			name: "identity-agnostic claim with no bound identity is denied",
			attr: &authorizer.AttributesRecord{
				User:     &user.DefaultInfo{},
				APIGroup: "claimedGroup",
				Resource: "claimedResource",
			},
			apidomainKey: "foo/bar",
			cluster:      &genericapirequest.Cluster{Name: "consumerWorkspace"},
			getAPIExport: identityAgnosticClaimingExport,
			resolveIdentities: func(*apisv1alpha2.APIExport, logicalcluster.Name, bool, schema.GroupResource) ([]string, error) {
				return nil, nil
			},

			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   `no APIBinding providing claimed resource "claimedResource.claimedGroup" found for identity-agnostic claim of API export: "fooExport"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := dynamiccontext.WithAPIDomainKey(context.Background(), dynamiccontext.APIDomainKey(tc.apidomainKey))
			if tc.cluster != nil {
				ctx = genericapirequest.WithCluster(ctx, *tc.cluster)
			}
			isBuiltIn := tc.isBuiltIn
			if isBuiltIn == nil {
				isBuiltIn = func(apis.GroupResource) bool { return false }
			}
			auth := &maximalPermissionAuthorizer{
				getAPIExport:            tc.getAPIExport,
				getAPIExportsByIdentity: tc.getAPIExportsByIdentity,
				newDeepSARAuthorizer:    tc.newDeepSARAuthorizer,
				resolveIdentities:       tc.resolveIdentities,
				isBuiltIn:               isBuiltIn,
			}
			dec, reason, err := auth.Authorize(ctx, tc.attr)
			errString := ""
			if err != nil {
				errString = err.Error()
			}
			require.Equal(t, tc.expectedErr, errString)
			require.Equal(t, tc.expectedDecision, dec)
			require.Equal(t, tc.expectedReason, reason)
		})
	}
}

// identityAgnosticClaimingExport is an APIExport claiming claimedResource.claimedGroup
// with no identity hash, i.e. an identity-agnostic claim.
func identityAgnosticClaimingExport(string, string) (*apisv1alpha2.APIExport, error) {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "fooExport",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "someWorkspace"},
		},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "claimedGroup",
						Resource: "claimedResource",
					},
				},
			},
		},
	}, nil
}

// providerExportWithLocalPolicy is a producing APIExport carrying a local
// maximal permission policy.
func providerExportWithLocalPolicy() *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "fooExport",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "someWorkspace"},
		},
		Spec: apisv1alpha2.APIExportSpec{
			MaximalPermissionPolicy: &apisv1alpha2.MaximalPermissionPolicy{Local: &apisv1alpha2.LocalAPIExportPolicy{}},
		},
	}
}
