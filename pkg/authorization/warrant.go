/*
Copyright 2024 The KCP Authors.

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
	"strings"

	"k8s.io/apimachinery/pkg/util/json"
	authserviceaccount "k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"
)

// WithWarrants wraps an authorizer and allows for additional permissions to be granted to users
// through warrants. If the base user info is not allowed, the warrants are checked.
func WithWarrants(base authorizer.Authorizer) authorizer.Authorizer {
	var recursive authorizer.Authorizer
	recursive = authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		cluster := genericapirequest.ClusterFrom(ctx)

		baseDec := authorizer.DecisionNoOpinion
		baseReason := "out-of-scope user"
		if validation.IsInScope(a.GetUser(), cluster.Name) {
			var err error
			baseDec, baseReason, err = base.Authorize(ctx, a)
			if err != nil {
				return authorizer.DecisionNoOpinion, "", err
			}
			if baseDec == authorizer.DecisionAllow || baseDec == authorizer.DecisionDeny {
				return baseDec, baseReason, err
			}
		} else if validation.IsServiceAccount(a.GetUser()) && validation.IsForeign(a.GetUser(), cluster.Name) {
			baseReason = "foreign service account" // prettier message for the common case
		}

		var reasons []string
		for _, v := range a.GetUser().GetExtra()[validation.WarrantExtraKey] {
			var w validation.Warrant
			if err := json.Unmarshal([]byte(v), &w); err != nil {
				reasons = append(reasons, "failed to unmarshal warrant: "+err.Error())
				continue
			}

			wu := &user.DefaultInfo{
				Name:   w.User,
				UID:    w.UID,
				Groups: w.Groups,
				Extra:  w.Extra,
			}
			if validation.IsServiceAccount(wu) && len(w.Extra[authserviceaccount.ClusterNameKey]) == 0 {
				reasons = append(reasons, "skipping warrant for serviceaccount without cluster scope")
				continue
			}

			dec, reason, err := recursive.Authorize(ctx, withDifferentUser{Attributes: a, user: wu})
			if err != nil {
				return authorizer.DecisionNoOpinion, "", err
			}
			reasons = append(reasons, reason) // denies of warrants are no reason to deny the base user.
			if dec == authorizer.DecisionAllow {
				baseDec = dec // override no-opinion
				break
			}
		}

		if len(reasons) > 0 {
			return baseDec, fmt.Sprintf("%s; warrants: [%s]", baseReason, strings.Join(reasons, "; ")), nil
		}
		return baseDec, baseReason, nil
	})

	return recursive
}

type withDifferentUser struct {
	authorizer.Attributes
	user user.Info
}

func (a withDifferentUser) GetUser() user.Info {
	return a.user
}

// ResolverWithWarrants wraps an authorizer.RuleResolver and allows for additional
// permissions to be granted to users through warrants. For that it unfolds the
// warrants and calls the base resolver for each of them.
func ResolverWithWarrants(base authorizer.RuleResolver) authorizer.RuleResolver {
	var recursive authorizer.RuleResolver
	recursive = RuleResolverFunc(func(ctx context.Context, u user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
		cluster := genericapirequest.ClusterFrom(ctx)

		var resourceRules []authorizer.ResourceRuleInfo
		var nonResourceRules []authorizer.NonResourceRuleInfo
		var incomplete bool

		if validation.IsInScope(u, cluster.Name) {
			bResourceRules, bNonResourceRules, bIncomplete, err := base.RulesFor(ctx, u, namespace)
			if err != nil {
				return nil, nil, false, err
			}
			if bIncomplete {
				incomplete = true
			}
			resourceRules = append(resourceRules, bResourceRules...)
			nonResourceRules = append(nonResourceRules, bNonResourceRules...)
		}

		warrants := u.GetExtra()[validation.WarrantExtraKey]
		if len(warrants) == 0 {
			return resourceRules, nonResourceRules, incomplete, nil
		}

		for _, v := range warrants {
			var w validation.Warrant
			if err := json.Unmarshal([]byte(v), &w); err != nil {
				return nil, nil, false, fmt.Errorf("failed to unmarshal warrant: %v", err)
			}

			wu := &user.DefaultInfo{
				Name:   w.User,
				UID:    w.UID,
				Groups: w.Groups,
				Extra:  w.Extra,
			}
			if validation.IsServiceAccount(wu) && len(w.Extra[authserviceaccount.ClusterNameKey]) == 0 {
				return nil, nil, false, fmt.Errorf("skipping warrant for serviceaccount without cluster scope")
			}

			resource, nonResource, wIncomplete, err := recursive.RulesFor(ctx, wu, namespace)
			if err != nil {
				return nil, nil, false, err
			}
			if wIncomplete {
				// aggregate for all warrants and the base user
				incomplete = true
			}
			if len(resource) > 0 {
				resourceRules = append(resourceRules, resource...)
			}
			if len(nonResource) > 0 {
				nonResourceRules = append(nonResourceRules, nonResource...)
			}
		}

		resourceRules, err := removeDuplicateResources(append(resourceRules, resourceRules...))
		if err != nil {
			return nil, nil, false, err
		}
		nonResourceRules, err = removeDuplicateNonResources(append(nonResourceRules, nonResourceRules...))
		if err != nil {
			return nil, nil, false, err
		}

		return resourceRules, nonResourceRules, incomplete, nil
	})

	return recursive
}

// RuleResolverFunc is a convenience type to implement RuleResolver with a function.
type RuleResolverFunc func(ctx context.Context, user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error)

func (f RuleResolverFunc) RulesFor(ctx context.Context, user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	return f(ctx, user, namespace)
}

func removeDuplicateResources(rules []authorizer.ResourceRuleInfo) ([]authorizer.ResourceRuleInfo, error) {
	seen := make(map[string]struct{})
	result := make([]authorizer.ResourceRuleInfo, 0, len(rules))
	for _, rule := range rules {
		bs, err := json.Marshal(&authorizer.DefaultResourceRuleInfo{
			Verbs:         rule.GetVerbs(),
			APIGroups:     rule.GetAPIGroups(),
			Resources:     rule.GetResources(),
			ResourceNames: rule.GetResourceNames(),
		})
		if err != nil {
			return nil, err
		}
		key := string(bs)
		if _, found := seen[key]; !found {
			result = append(result, rule)
			seen[key] = struct{}{}
		}
	}
	return result, nil
}

func removeDuplicateNonResources(rules []authorizer.NonResourceRuleInfo) ([]authorizer.NonResourceRuleInfo, error) {
	seen := make(map[string]struct{})
	result := make([]authorizer.NonResourceRuleInfo, 0, len(rules))
	for _, rule := range rules {
		bs, err := json.Marshal(&authorizer.DefaultNonResourceRuleInfo{
			Verbs:           rule.GetVerbs(),
			NonResourceURLs: rule.GetNonResourceURLs(),
		})
		if err != nil {
			return nil, err
		}
		key := string(bs)
		if _, found := seen[key]; !found {
			result = append(result, rule)
			seen[key] = struct{}{}
		}
	}
	return result, nil
}
