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

package ruleset

import (
	"fmt"
	"regexp"

	"sigs.k8s.io/yaml"
)

type RuleSetGetter func() (RuleSet, bool)

type matchFunc func(string) bool

type rawRuleSet struct {
	DeniedUserNames  []string `json:"denied_user_names,omitempty"`
	AllowedUserNames []string `json:"allowed_user_names,omitempty"`
}

// RuleSet holds rules for determining whether a user is authorized to perform
// some action. Rules come in two types: deny and allow. Each rule's
// raw form is a regular expression string. A RuleSet holds the corresponding
// rexexp string matching functions.
type RuleSet struct {
	denyMatchers  []matchFunc
	allowMatchers []matchFunc
}

// UserNameIsAllowed applies a RuleSet to the given name to determine whether
// the user is authorized perform some action. Deny rules are applied first, and
// a matching user name is immediately denied. Allow rules are applied next, and
// only a matching user name is allowed.
func (rs *RuleSet) UserNameIsAllowed(name string) bool {
	// deny rules get priority
	for _, denied := range rs.denyMatchers {
		if denied(name) {
			return false
		}
	}

	// only allow requests with user names that match to continue
	for _, allowed := range rs.allowMatchers {
		if allowed(name) {
			return true
		}
	}
	return false
}

// FromYAML populates a RuleSet from yaml data
func FromYAML(data []byte) (*RuleSet, error) {
	var rawRules rawRuleSet
	if err := yaml.Unmarshal(data, &rawRules); err != nil {
		return nil, fmt.Errorf("failed to unmarshal gating rules: %w", err)
	}

	rules := RuleSet{}

	// populate disallow patterns
	for _, s := range rawRules.DeniedUserNames {
		r, err := regexp.Compile(s)
		if err != nil {
			return nil, err
		}
		rules.denyMatchers = append(rules.denyMatchers, r.MatchString)
	}

	// populate allow patterns
	for _, s := range rawRules.AllowedUserNames {
		r, err := regexp.Compile(s)
		if err != nil {
			return nil, err
		}
		rules.allowMatchers = append(rules.allowMatchers, r.MatchString)
	}

	return &rules, nil
}
