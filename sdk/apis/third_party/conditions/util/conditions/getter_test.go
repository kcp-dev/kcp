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

var (
	nil1          *conditionsapi.Condition
	true1         = TrueCondition("true1")
	unknown1      = UnknownCondition("unknown1", "reason unknown1", "message unknown1")
	falseInfo1    = FalseCondition("falseInfo1", "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1")
	falseWarning1 = FalseCondition("falseWarning1", "reason falseWarning1", conditionsapi.ConditionSeverityWarning, "message falseWarning1")
	falseError1   = FalseCondition("falseError1", "reason falseError1", conditionsapi.ConditionSeverityError, "message falseError1")
)

func newConditioned(name string) *conditioned {
	return &conditioned{
		Unstructured: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind": "Foo",
				"metadata": map[string]interface{}{
					"name": name,
				},
			},
		},
	}
}

type conditioned struct {
	*unstructured.Unstructured
	conditions conditionsapi.Conditions
}

func (c *conditioned) SetConditions(conditions conditionsapi.Conditions) {
	c.conditions = conditions
}

func (c *conditioned) GetConditions() conditionsapi.Conditions {
	return c.conditions
}

var _ Getter = &conditioned{}
var _ Setter = &conditioned{}

func TestGetAndHas(t *testing.T) {
	g := NewWithT(t)

	cluster := newConditioned("test")

	g.Expect(Has(cluster, "conditionBaz")).To(BeFalse())
	g.Expect(Get(cluster, "conditionBaz")).To(BeNil())

	cluster.SetConditions(conditionList(TrueCondition("conditionBaz")))

	g.Expect(Has(cluster, "conditionBaz")).To(BeTrue())
	g.Expect(Get(cluster, "conditionBaz")).To(HaveSameStateOf(TrueCondition("conditionBaz")))
}

func TestIsMethods(t *testing.T) {
	g := NewWithT(t)

	obj := getterWithConditions(nil1, true1, unknown1, falseInfo1, falseWarning1, falseError1)

	// test isTrue
	g.Expect(IsTrue(obj, "nil1")).To(BeFalse())
	g.Expect(IsTrue(obj, "true1")).To(BeTrue())
	g.Expect(IsTrue(obj, "falseInfo1")).To(BeFalse())
	g.Expect(IsTrue(obj, "unknown1")).To(BeFalse())

	// test isFalse
	g.Expect(IsFalse(obj, "nil1")).To(BeFalse())
	g.Expect(IsFalse(obj, "true1")).To(BeFalse())
	g.Expect(IsFalse(obj, "falseInfo1")).To(BeTrue())
	g.Expect(IsFalse(obj, "unknown1")).To(BeFalse())

	// test isUnknown
	g.Expect(IsUnknown(obj, "nil1")).To(BeTrue())
	g.Expect(IsUnknown(obj, "true1")).To(BeFalse())
	g.Expect(IsUnknown(obj, "falseInfo1")).To(BeFalse())
	g.Expect(IsUnknown(obj, "unknown1")).To(BeTrue())

	// test GetReason
	g.Expect(GetReason(obj, "nil1")).To(Equal(""))
	g.Expect(GetReason(obj, "falseInfo1")).To(Equal("reason falseInfo1"))

	// test GetMessage
	g.Expect(GetMessage(obj, "nil1")).To(Equal(""))
	g.Expect(GetMessage(obj, "falseInfo1")).To(Equal("message falseInfo1"))

	// test GetSeverity
	g.Expect(GetSeverity(obj, "nil1")).To(BeNil())
	severity := GetSeverity(obj, "falseInfo1")
	expectedSeverity := conditionsapi.ConditionSeverityInfo
	g.Expect(severity).To(Equal(&expectedSeverity))

	// test GetMessage
	g.Expect(GetLastTransitionTime(obj, "nil1")).To(BeNil())
	g.Expect(GetLastTransitionTime(obj, "falseInfo1")).ToNot(BeNil())
}

func TestMirror(t *testing.T) {
	foo := FalseCondition("foo", "reason foo", conditionsapi.ConditionSeverityInfo, "message foo")
	ready := TrueCondition(conditionsapi.ReadyCondition)
	readyBar := ready.DeepCopy()
	readyBar.Type = "bar"

	tests := []struct {
		name string
		from Getter
		t    conditionsapi.ConditionType
		want *conditionsapi.Condition
	}{
		{
			name: "Returns nil when the ready condition does not exists",
			from: getterWithConditions(foo),
			want: nil,
		},
		{
			name: "Returns ready condition from source",
			from: getterWithConditions(ready, foo),
			t:    "bar",
			want: readyBar,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			got := mirror(tt.from, tt.t)
			if tt.want == nil {
				g.Expect(got).To(BeNil())
				return
			}
			g.Expect(got).To(HaveSameStateOf(tt.want))
		})
	}
}

func TestSummary(t *testing.T) {
	foo := TrueCondition("foo")
	bar := FalseCondition("bar", "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1")
	baz := FalseCondition("baz", "reason falseInfo2", conditionsapi.ConditionSeverityInfo, "message falseInfo2")
	existingReady := FalseCondition(conditionsapi.ReadyCondition, "reason falseError1", conditionsapi.ConditionSeverityError, "message falseError1") // NB. existing ready has higher priority than other conditions

	tests := []struct {
		name    string
		from    Getter
		options []MergeOption
		want    *conditionsapi.Condition
	}{
		{
			name: "Returns nil when there are no conditions to summarize",
			from: getterWithConditions(),
			want: nil,
		},
		{
			name: "Returns ready condition with the summary of existing conditions (with default options)",
			from: getterWithConditions(foo, bar),
			want: FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1"),
		},
		{
			name:    "Returns ready condition with the summary of existing conditions (using WithStepCounter options)",
			from:    getterWithConditions(foo, bar),
			options: []MergeOption{WithStepCounter()},
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "1 of 2 completed"),
		},
		{
			name:    "Returns ready condition with the summary of existing conditions (using WithStepCounterIf options)",
			from:    getterWithConditions(foo, bar),
			options: []MergeOption{WithStepCounterIf(false)},
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1"),
		},
		{
			name:    "Returns ready condition with the summary of existing conditions (using WithStepCounterIf options)",
			from:    getterWithConditions(foo, bar),
			options: []MergeOption{WithStepCounterIf(true)},
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "1 of 2 completed"),
		},
		{
			name:    "Returns ready condition with the summary of existing conditions (using WithStepCounterIf and WithStepCounterIfOnly options)",
			from:    getterWithConditions(bar),
			options: []MergeOption{WithStepCounter(), WithStepCounterIfOnly("bar")},
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "0 of 1 completed"),
		},
		{
			name:    "Returns ready condition with the summary of existing conditions (using WithStepCounterIf and WithStepCounterIfOnly options)",
			from:    getterWithConditions(foo, bar),
			options: []MergeOption{WithStepCounter(), WithStepCounterIfOnly("foo")},
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1"),
		},
		{
			name:    "Returns ready condition with the summary of selected conditions (using WithConditions options)",
			from:    getterWithConditions(foo, bar),
			options: []MergeOption{WithConditions("foo")}, // bar should be ignored
			want:    TrueCondition(conditionsapi.ReadyCondition),
		},
		{
			name:    "Returns ready condition with the summary of selected conditions (using WithConditions and WithStepCounter options)",
			from:    getterWithConditions(foo, bar, baz),
			options: []MergeOption{WithConditions("foo", "bar"), WithStepCounter()}, // baz should be ignored, total steps should be 2
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "1 of 2 completed"),
		},
		{
			name:    "Returns ready condition with the summary of selected conditions (using WithConditions and WithStepCounterIfOnly options)",
			from:    getterWithConditions(bar),
			options: []MergeOption{WithConditions("bar", "baz"), WithStepCounter(), WithStepCounterIfOnly("bar")}, // there is only bar, the step counter should be set and counts only a subset of conditions
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "0 of 1 completed"),
		},
		{
			name:    "Returns ready condition with the summary of selected conditions (using WithConditions and WithStepCounterIfOnly options - with inconsistent order between the two)",
			from:    getterWithConditions(bar),
			options: []MergeOption{WithConditions("baz", "bar"), WithStepCounter(), WithStepCounterIfOnly("bar", "baz")}, // conditions in WithStepCounterIfOnly could be in different order than in WithConditions
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "0 of 2 completed"),
		},
		{
			name:    "Returns ready condition with the summary of selected conditions (using WithConditions and WithStepCounterIfOnly options)",
			from:    getterWithConditions(bar, baz),
			options: []MergeOption{WithConditions("bar", "baz"), WithStepCounter(), WithStepCounterIfOnly("bar")}, // there is also baz, so the step counter should not be set
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1"),
		},
		{
			name:    "Ready condition respects merge order",
			from:    getterWithConditions(bar, baz),
			options: []MergeOption{WithConditions("baz", "bar")}, // baz should take precedence on bar
			want:    FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo2", conditionsapi.ConditionSeverityInfo, "message falseInfo2"),
		},
		{
			name: "Ignores existing Ready condition when computing the summary",
			from: getterWithConditions(existingReady, foo, bar),
			want: FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			got := summary(tt.from, tt.options...)
			if tt.want == nil {
				g.Expect(got).To(BeNil())
				return
			}
			g.Expect(got).To(HaveSameStateOf(tt.want))
		})
	}
}

func TestAggregate(t *testing.T) {
	ready1 := TrueCondition(conditionsapi.ReadyCondition)
	ready2 := FalseCondition(conditionsapi.ReadyCondition, "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "message falseInfo1")
	bar := FalseCondition("bar", "reason falseError1", conditionsapi.ConditionSeverityError, "message falseError1") // NB. bar has higher priority than other conditions

	tests := []struct {
		name string
		from []Getter
		t    conditionsapi.ConditionType
		want *conditionsapi.Condition
	}{
		{
			name: "Returns nil when there are no conditions to aggregate",
			from: []Getter{},
			want: nil,
		},
		{
			name: "Returns foo condition with the aggregation of object's ready conditions",
			from: []Getter{
				getterWithConditions(ready1),
				getterWithConditions(ready1),
				getterWithConditions(ready2, bar),
				getterWithConditions(),
				getterWithConditions(bar),
			},
			t:    "foo",
			want: FalseCondition("foo", "reason falseInfo1", conditionsapi.ConditionSeverityInfo, "2 of 5 completed"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)

			got := aggregate(tt.from, tt.t)
			if tt.want == nil {
				g.Expect(got).To(BeNil())
				return
			}
			g.Expect(got).To(HaveSameStateOf(tt.want))
		})
	}
}

func getterWithConditions(conditions ...*conditionsapi.Condition) Getter {
	obj := newConditioned("test")
	obj.SetConditions(conditionList(conditions...))
	return obj
}

func conditionList(conditions ...*conditionsapi.Condition) conditionsapi.Conditions {
	cs := conditionsapi.Conditions{}
	for _, x := range conditions {
		if x != nil {
			cs = append(cs, *x)
		}
	}
	return cs
}
