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
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// IntersectionAuthorizer is a type that implements an authorizer where all given authorizers must allow.
// If at least one authorizer denies, this authorizer denies. Authorizers having no opinion are skipped.
// If the given authorizers are empty list, this authorizer has no opinion.
// If at least one authorizer has errors, this authorizer has no opinion and returns a new aggregate of all occurred errors.
type IntersectionAuthorizer []authorizer.Authorizer

func (auth IntersectionAuthorizer) Authorize(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	var (
		errors  []error
		reasons []string
		allowed bool
	)

	for _, curr := range auth {
		decision, reason, err := curr.Authorize(ctx, a)

		if err != nil {
			errors = append(errors, err)
		}
		if len(reason) != 0 {
			reasons = append(reasons, reason)
		}
		switch decision {
		case authorizer.DecisionAllow:
			allowed = true
		case authorizer.DecisionDeny:
			return decision, reason, err
		case authorizer.DecisionNoOpinion:
			// continue to the next authorizer
		}
	}

	if len(errors) > 0 {
		return authorizer.DecisionNoOpinion, strings.Join(reasons, "\n"), utilerrors.NewAggregate(errors)
	}

	if allowed {
		return authorizer.DecisionAllow, strings.Join(reasons, "\n"), nil
	}

	return authorizer.DecisionNoOpinion, strings.Join(reasons, "\n"), nil
}
