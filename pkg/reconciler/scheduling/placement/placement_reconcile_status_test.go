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

package placement

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestSetScheduledCondition(t *testing.T) {
	testCases := map[string]struct {
		labels      map[string]string
		annotations map[string]string
		scheduled   bool
		reason      conditionsapi.ConditionType
	}{
		"disabled label true": {
			labels: map[string]string{
				workloadv1alpha1.SchedulingDisabledLabel: "true",
			},
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: `{"foo":"Pending"}`,
			},
			reason: NamespaceReasonSchedulingDisabled,
		},
		"disabled label empty": {
			labels: map[string]string{
				workloadv1alpha1.SchedulingDisabledLabel: "",
			},
		},
		"disabled label false": {
			labels: map[string]string{
				workloadv1alpha1.SchedulingDisabledLabel: "false",
			},
		},
		"scheduled": {
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: `{"foo":"Pending"}`,
			},
			scheduled: true,
		},
		"unschedulable": {
			annotations: map[string]string{
				schedulingv1alpha1.PlacementAnnotationKey: `{}`,
			},
			reason: NamespaceReasonUnschedulable,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      testCase.labels,
					Annotations: testCase.annotations,
				},
			}
			updatedNs := setScheduledCondition(ns)

			if !testCase.scheduled && testCase.reason == "" {
				c := conditions.Get(&NamespaceConditionsAdapter{updatedNs}, NamespaceScheduled)
				require.Nil(t, c)
			} else {
				c := conditions.Get(&NamespaceConditionsAdapter{updatedNs}, NamespaceScheduled)
				require.NotNil(t, c)
				scheduled := c.Status == corev1.ConditionTrue
				require.Equal(t, testCase.scheduled, scheduled, "unexpected value for scheduled")
				if len(testCase.reason) > 0 {
					require.Equal(t, string(testCase.reason), c.Reason, "unexpected reason")
				}
			}
		})
	}
}
