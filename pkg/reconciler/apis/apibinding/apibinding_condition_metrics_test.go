/*
Copyright 2026 The kcp Authors.

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

package apibinding

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func bindingWithConditions(cluster, name string, conds ...conditionsv1alpha1.Condition) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
		},
		Status: apisv1alpha2.APIBindingStatus{Conditions: conds},
	}
}

func cond(condType conditionsv1alpha1.ConditionType, status corev1.ConditionStatus) conditionsv1alpha1.Condition {
	return conditionsv1alpha1.Condition{Type: condType, Status: status}
}

func TestHandleConditionMetricsOnAdd(t *testing.T) {
	t.Parallel()
	t.Run("conditions are tracked on add", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		b := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
			cond(apisv1alpha2.InitialBindingCompleted, corev1.ConditionFalse),
		)
		c.handleConditionMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindingConditions, 1)
		for _, snapshot := range c.countedAPIBindingConditions {
			require.Equal(t, string(corev1.ConditionTrue), snapshot[string(apisv1alpha2.APIExportValid)])
			require.Equal(t, string(corev1.ConditionFalse), snapshot[string(apisv1alpha2.InitialBindingCompleted)])
		}
	})

	t.Run("binding with no conditions stores empty snapshot", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		b := bindingWithConditions("root:ws", "test")
		c.handleConditionMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindingConditions, 1)
		for _, snapshot := range c.countedAPIBindingConditions {
			require.Empty(t, snapshot)
		}
	})

	t.Run("duplicate add is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		b := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(b)
		c.handleConditionMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindingConditions, 1)
	})
}

func TestHandleConditionMetricsOnUpdate(t *testing.T) {
	t.Parallel()
	t.Run("condition status change is tracked", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionFalse),
		)
		new := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIBindingConditions {
			require.Equal(t, string(corev1.ConditionTrue), snapshot[string(apisv1alpha2.APIExportValid)])
		}
	})

	t.Run("new condition added on update", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		new := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
			cond(apisv1alpha2.InitialBindingCompleted, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIBindingConditions {
			require.Len(t, snapshot, 2)
		}
	})

	t.Run("removed condition is cleaned up on update", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
			cond(apisv1alpha2.InitialBindingCompleted, corev1.ConditionTrue),
		)
		new := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIBindingConditions {
			require.Len(t, snapshot, 1)
			require.NotContains(t, snapshot, string(apisv1alpha2.InitialBindingCompleted))
		}
	})
}

func TestHandleConditionMetricsOnDelete(t *testing.T) {
	t.Parallel()
	t.Run("delete removes all condition tracking", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		b := bindingWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindingConditions, 1)
		c.handleConditionMetricsOnDelete(b)
		require.Empty(t, c.countedAPIBindingConditions)
	})

	t.Run("delete of unknown binding is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		b := bindingWithConditions("root:ws", "unknown",
			cond(apisv1alpha2.APIExportValid, corev1.ConditionTrue),
		)
		require.NotPanics(t, func() { c.handleConditionMetricsOnDelete(b) })
	})
}
