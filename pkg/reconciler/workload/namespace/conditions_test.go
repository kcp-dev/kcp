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

package namespace

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestSetScheduledCondition(t *testing.T) {
	testCases := map[string]struct {
		labels    map[string]string
		scheduled bool
		reason    conditionsapi.ConditionType
	}{
		"disabled label present but empty": {
			labels: map[string]string{
				SchedulingDisabledLabel:                  "",
				DeprecatedScheduledClusterNamespaceLabel: "foo",
			},
			reason: NamespaceReasonSchedulingDisabled,
		},
		"disabled label present but not empty": {
			labels: map[string]string{
				SchedulingDisabledLabel:                  "false",
				DeprecatedScheduledClusterNamespaceLabel: "foo",
			},
			reason: NamespaceReasonSchedulingDisabled,
		},
		"scheduled": {
			labels: map[string]string{
				DeprecatedScheduledClusterNamespaceLabel: "foo",
			},
			scheduled: true,
		},
		"unscheduled with label": {
			labels: map[string]string{
				DeprecatedScheduledClusterNamespaceLabel: "",
			},
			reason: NamespaceReasonUnschedulable,
		},
		"unscheduled without label": {
			reason: NamespaceReasonUnschedulable,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: testCase.labels,
				},
			}
			updatedNs := setScheduledCondition(ns)
			condition := conditions.Get(&NamespaceConditionsAdapter{updatedNs}, NamespaceScheduled)
			require.NotEmpty(t, condition, "condition missing")
			scheduled := condition.Status == corev1.ConditionTrue
			require.Equal(t, testCase.scheduled, scheduled, "unexpected value for scheduled")
			if len(testCase.reason) > 0 {
				require.Equal(t, string(testCase.reason), condition.Reason, "unexpected reason")
			}
		})
	}
}
