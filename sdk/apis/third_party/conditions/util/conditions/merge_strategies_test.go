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
	"testing"

	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	conditionsapi "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestGetStepCounterMessage(t *testing.T) {
	g := NewWithT(t)

	groups := getConditionGroups(conditionsWithSource(&conditioned{},
		nil1,
		true1, true1,
		falseInfo1,
		falseWarning1, falseWarning1,
		falseError1,
		unknown1,
	))

	got := getStepCounterMessage(groups, 8)

	// step count message should report n° if true conditions over to number
	g.Expect(got).To(Equal("2 of 8 completed"))
}

func TestLocalizeReason(t *testing.T) {
	g := NewWithT(t)

	getter := &conditioned{
		Unstructured: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Foo",
				"metadata": map[string]interface{}{
					"name": "test-cluster",
				},
			},
		},
	}

	// localize should reason location
	got := localizeReason("foo", getter)
	g.Expect(got).To(Equal("foo @ Foo/test-cluster"))

	// localize should not alter existing location
	got = localizeReason("foo @ SomeKind/some-name", getter)
	g.Expect(got).To(Equal("foo @ SomeKind/some-name"))
}

func TestGetFirstReasonAndMessage(t *testing.T) {
	g := NewWithT(t)

	foo := FalseCondition("foo", "falseFoo", conditionsapi.ConditionSeverityInfo, "message falseFoo")
	bar := FalseCondition("bar", "falseBar", conditionsapi.ConditionSeverityInfo, "message falseBar")

	getter := &conditioned{
		Unstructured: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Foo",
				"metadata": map[string]interface{}{
					"name": "test-cluster",
				},
			},
		},
	}

	groups := getConditionGroups(conditionsWithSource(getter, foo, bar))

	// getFirst should report first condition in lexicografical order if no order is specified
	gotReason := getFirstReason(groups, nil, false)
	g.Expect(gotReason).To(Equal("falseBar"))
	gotMessage := getFirstMessage(groups, nil)
	g.Expect(gotMessage).To(Equal("message falseBar"))

	// getFirst should report should respect order
	gotReason = getFirstReason(groups, []conditionsapi.ConditionType{"foo", "bar"}, false)
	g.Expect(gotReason).To(Equal("falseFoo"))
	gotMessage = getFirstMessage(groups, []conditionsapi.ConditionType{"foo", "bar"})
	g.Expect(gotMessage).To(Equal("message falseFoo"))

	// getFirst should report should respect order in case of missing conditions
	gotReason = getFirstReason(groups, []conditionsapi.ConditionType{"missingBaz", "foo", "bar"}, false)
	g.Expect(gotReason).To(Equal("falseFoo"))
	gotMessage = getFirstMessage(groups, []conditionsapi.ConditionType{"missingBaz", "foo", "bar"})
	g.Expect(gotMessage).To(Equal("message falseFoo"))

	// getFirst should fallback to first condition if none of the conditions in the list exists
	gotReason = getFirstReason(groups, []conditionsapi.ConditionType{"missingBaz"}, false)
	g.Expect(gotReason).To(Equal("falseBar"))
	gotMessage = getFirstMessage(groups, []conditionsapi.ConditionType{"missingBaz"})
	g.Expect(gotMessage).To(Equal("message falseBar"))

	// getFirstReason should localize reason if required
	gotReason = getFirstReason(groups, nil, true)
	g.Expect(gotReason).To(Equal("falseBar @ Foo/test-cluster"))
}
