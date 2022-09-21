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
	"fmt"

	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// NewAnonymizer returns an authorizer anonymizing authorization decisions and error cases of a delegate authorizer.
func NewAnonymizer(authorizerName string, delegate authorizer.Authorizer) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		dec, reason, err := delegate.Authorize(ctx, attr)

		switch dec {
		case authorizer.DecisionAllow:
			reason = "access granted"
		case authorizer.DecisionDeny:
			reason = "access denied"
		case authorizer.DecisionNoOpinion:
			reason = "access denied"
		}

		if err != nil {
			return dec, reason, fmt.Errorf("an error occurred in: %v", authorizerName)
		}

		return dec, reason, nil
	})
}
