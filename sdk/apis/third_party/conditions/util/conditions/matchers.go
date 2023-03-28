/*
Copyright 2020 The Kubernetes Authors.

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

package conditions

import (
	"errors"

	"github.com/onsi/gomega/format"
	"github.com/onsi/gomega/types"

	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// HaveSameStateOf matches a condition to have the same state of another.
func HaveSameStateOf(expected *conditionsapi.Condition) types.GomegaMatcher {
	return &conditionMatcher{
		Expected: expected,
	}
}

type conditionMatcher struct {
	Expected *conditionsapi.Condition
}

func (matcher *conditionMatcher) Match(actual interface{}) (success bool, err error) {
	actualCondition, ok := actual.(*conditionsapi.Condition)
	if !ok {
		return false, errors.New("value should be a condition")
	}

	return hasSameState(actualCondition, matcher.Expected), nil
}

func (matcher *conditionMatcher) FailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "to have the same state of", matcher.Expected)
}
func (matcher *conditionMatcher) NegatedFailureMessage(actual interface{}) (message string) {
	return format.Message(actual, "not to have the same state of", matcher.Expected)
}
