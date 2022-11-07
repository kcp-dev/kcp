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

package usergating

import (
	"context"
	"net/http"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/proxy/usergating/ruleset"
)

type UserGate struct {
	GetRuleSet ruleset.RuleSetGetter
}

func WithUserGating(delegate http.Handler, failed http.Handler, getRuleSet ruleset.RuleSetGetter) http.Handler {
	gate := &UserGate{
		GetRuleSet: getRuleSet,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		u, ok := request.UserFrom(ctx)
		if !ok {
			delegate.ServeHTTP(w, r)
			return
		}

		decision := gate.Decide(ctx, u)
		if decision != authorizer.DecisionDeny {
			delegate.ServeHTTP(w, r)
			return
		}

		failed.ServeHTTP(w, r)
	})
}

// Decide whether the user is allowed to make the request
func (a *UserGate) Decide(ctx context.Context, info user.Info) authorizer.Decision {
	logger := klog.FromContext(ctx).WithValues("component", "usergate")
	logger.V(1).Info("checking auth", "user", info.GetName())

	if rules, found := a.GetRuleSet(); found {
		if rules.UserNameIsAllowed(info.GetName()) {
			return authorizer.DecisionAllow
		}
		return authorizer.DecisionDeny
	}
	return authorizer.DecisionNoOpinion
}
