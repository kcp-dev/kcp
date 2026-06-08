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

package apiexport

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func newTestController() *controller {
	return &controller{
		countedAPIExportConditions: make(map[string]map[string]string),
		readyAPIExports:            make(map[string]struct{}),
	}
}

func exportWithConditions(cluster, name string, conds ...conditionsv1alpha1.Condition) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
		},
		Status: apisv1alpha2.APIExportStatus{Conditions: conds},
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
		e := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionFalse),
		)
		c.handleConditionMetricsOnAdd(e)
		require.Len(t, c.countedAPIExportConditions, 1)
		for _, snapshot := range c.countedAPIExportConditions {
			require.Equal(t, string(corev1.ConditionTrue), snapshot[string(apisv1alpha2.APIExportIdentityValid)])
			require.Equal(t, string(corev1.ConditionFalse), snapshot[string(apisv1alpha2.APIExportVirtualWorkspaceURLsReady)])
		}
	})

	t.Run("export with no conditions stores empty snapshot", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithConditions("root:ws", "test")
		c.handleConditionMetricsOnAdd(e)
		require.Len(t, c.countedAPIExportConditions, 1)
		for _, snapshot := range c.countedAPIExportConditions {
			require.Empty(t, snapshot)
		}
	})

	t.Run("duplicate add is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(e)
		c.handleConditionMetricsOnAdd(e)
		require.Len(t, c.countedAPIExportConditions, 1)
	})
}

func TestHandleConditionMetricsOnUpdate(t *testing.T) {
	t.Parallel()
	t.Run("condition status change is tracked", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionFalse),
		)
		new := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIExportConditions {
			require.Equal(t, string(corev1.ConditionTrue), snapshot[string(apisv1alpha2.APIExportIdentityValid)])
		}
	})

	t.Run("new condition added on update", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		new := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIExportConditions {
			require.Len(t, snapshot, 2)
		}
	})

	t.Run("removed condition is cleaned up on update", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		old := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		new := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(old)
		c.handleConditionMetricsOnUpdate(old, new)
		for _, snapshot := range c.countedAPIExportConditions {
			require.Len(t, snapshot, 1)
			require.NotContains(t, snapshot, string(apisv1alpha2.APIExportVirtualWorkspaceURLsReady))
		}
	})
}

func TestHandleConditionMetricsOnDelete(t *testing.T) {
	t.Parallel()
	t.Run("delete removes all condition tracking", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithConditions("root:ws", "test",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		c.handleConditionMetricsOnAdd(e)
		require.Len(t, c.countedAPIExportConditions, 1)
		c.handleConditionMetricsOnDelete(e)
		require.Empty(t, c.countedAPIExportConditions)
	})

	t.Run("delete of unknown export is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithConditions("root:ws", "unknown",
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
		)
		require.NotPanics(t, func() { c.handleConditionMetricsOnDelete(e) })
	})
}

func TestHandleConditionMetricsConcurrency(t *testing.T) {
	t.Parallel()
	c := newTestController()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "export-" + string(rune('a'+i%26))
			e := exportWithConditions("root:ws", name,
				cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			)
			c.handleConditionMetricsOnAdd(e)
		}(i)
	}
	wg.Wait()
}
