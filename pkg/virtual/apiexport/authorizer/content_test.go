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

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

func TestContentAuthorizer(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		contentDecision, expectedDecision authorizer.Decision
	}{
		{
			name:             "content access allowed",
			contentDecision:  authorizer.DecisionAllow,
			expectedDecision: authorizer.DecisionAllow,
		},
		{
			name:             "content access denied",
			contentDecision:  authorizer.DecisionDeny,
			expectedDecision: authorizer.DecisionDeny,
		},
		{
			name:             "content access no opinion",
			contentDecision:  authorizer.DecisionNoOpinion,
			expectedDecision: authorizer.DecisionNoOpinion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &apiExportsContentAuthorizer{
				newDelegatedAuthorizer: func(clusterName string) (authorizer.Authorizer, error) {
					return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
						return tc.contentDecision, "", nil
					}), nil
				},
				delegate: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
					return tc.contentDecision, "", nil
				}),
			}

			ctx := dynamiccontext.WithAPIDomainKey(context.Background(), dynamiccontext.APIDomainKey("foo/bar"))
			dec, _, err := auth.Authorize(ctx, &authorizer.AttributesRecord{
				User: &user.DefaultInfo{},
			})
			require.NoError(t, err)
			require.Equal(t, tc.expectedDecision, dec)
		})
	}
}
