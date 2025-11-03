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

package helpers

import (
	"fmt"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// Eventually asserts that given condition will be met in waitFor time, periodically checking target function
// each tick. In addition to require.Eventually, this function t.Logs the reason string value returned by the condition
// function (eventually after 20% of the wait time) to aid in debugging.
func Eventually(t TestingT, condition func() (success bool, reason string), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {
	t.Helper()

	var last string
	start := time.Now()
	require.Eventually(t, func() bool {
		t.Helper()

		ok, msg := condition()
		if time.Since(start) > waitFor/5 {
			if !ok && msg != "" && msg != last {
				last = msg
				t.Logf("Waiting for condition, but got: %s", msg)
			} else if ok && msg != "" && last != "" {
				t.Logf("Condition became true: %s", msg)
			}
		}
		return ok
	}, waitFor, tick, msgAndArgs...)
}

// EventuallyReady asserts that the object returned by getter() eventually has a ready condition.
func EventuallyReady(t TestingT, getter func() (conditions.Getter, error), msgAndArgs ...interface{}) {
	t.Helper()
	EventuallyCondition(t, getter, Is(conditionsv1alpha1.ReadyCondition), msgAndArgs...)
}

type ConditionEvaluator struct {
	conditionType   conditionsv1alpha1.ConditionType
	conditionStatus corev1.ConditionStatus
	conditionReason *string
}

func (c *ConditionEvaluator) matches(object conditions.Getter) (*conditionsv1alpha1.Condition, string, bool) {
	condition := conditions.Get(object, c.conditionType)
	if condition == nil {
		return nil, c.descriptor(), false
	}
	if condition.Status != c.conditionStatus {
		return condition, c.descriptor(), false
	}
	if c.conditionReason != nil && condition.Reason != *c.conditionReason {
		return condition, c.descriptor(), false
	}
	return condition, c.descriptor(), true
}

func (c *ConditionEvaluator) descriptor() string {
	var descriptor string
	switch c.conditionStatus {
	case corev1.ConditionTrue:
		descriptor = "to be"
	case corev1.ConditionFalse:
		descriptor = "not to be"
	case corev1.ConditionUnknown:
		descriptor = "to not know if it is"
	}
	descriptor += fmt.Sprintf(" %s", c.conditionType)
	if c.conditionReason != nil {
		descriptor += fmt.Sprintf(" (with reason %s)", *c.conditionReason)
	}
	return descriptor
}

func Is(conditionType conditionsv1alpha1.ConditionType) *ConditionEvaluator {
	return &ConditionEvaluator{
		conditionType:   conditionType,
		conditionStatus: corev1.ConditionTrue,
	}
}

func IsNot(conditionType conditionsv1alpha1.ConditionType) *ConditionEvaluator {
	return &ConditionEvaluator{
		conditionType:   conditionType,
		conditionStatus: corev1.ConditionFalse,
	}
}

func (c *ConditionEvaluator) WithReason(reason string) *ConditionEvaluator {
	c.conditionReason = &reason
	return c
}

// EventuallyCondition asserts that the object returned by getter() eventually has a condition that matches the evaluator.
func EventuallyCondition(t TestingT, getter func() (conditions.Getter, error), evaluator *ConditionEvaluator, msgAndArgs ...interface{}) {
	t.Helper()
	Eventually(t, func() (bool, string) {
		obj, err := getter()
		require.NoError(t, err, "Error fetching object")
		condition, descriptor, done := evaluator.matches(obj)
		var reason string
		if !done {
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for object %s: %s: %s", descriptor, condition.Reason, condition.Message)
			} else {
				reason = fmt.Sprintf("Not done waiting for object %s: no condition present", descriptor)
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, 100*time.Millisecond, msgAndArgs...)
}
