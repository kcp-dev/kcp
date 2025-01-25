/*
Copyright 2025 The KCP Authors.

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
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"
)

// WithWarrantsAndScopes flattens the user's warrants and applies scopes. It
// then calls the underlying authorizer for each users in the result.
func WithWarrantsAndScopes(authz authorizer.Authorizer) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		// flatten and scope.
		var clusterName logicalcluster.Name
		if cluster := request.ClusterFrom(ctx); cluster != nil {
			clusterName = cluster.Name
		}
		eus := rbacregistryvalidation.EffectiveUsers(clusterName, a.GetUser())

		// authorize like union authorizer does.
		var (
			errlist    []error
			reasonlist []string
		)
		for _, eu := range eus {
			decision, reason, err := authz.Authorize(ctx, withOtherUser{Attributes: a, user: eu})
			if err != nil {
				errlist = append(errlist, err)
			}
			if len(reason) != 0 {
				reasonlist = append(reasonlist, reason)
			}
			switch decision {
			case authorizer.DecisionAllow, authorizer.DecisionDeny:
				return decision, reason, err
			case authorizer.DecisionNoOpinion:
				// continue to the next authorizer
			}
		}

		return authorizer.DecisionNoOpinion, strings.Join(reasonlist, "\n"), utilerrors.NewAggregate(errlist)
	})
}

type withOtherUser struct {
	authorizer.Attributes
	user user.Info
}

func (w withOtherUser) GetUser() user.Info {
	return w.user
}
