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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func exportWithCreation(cluster, name string, created time.Time, conds ...conditionsv1alpha1.Condition) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Annotations:       map[string]string{logicalcluster.AnnotationKey: cluster},
			CreationTimestamp: metav1.Time{Time: created},
		},
		Status: apisv1alpha2.APIExportStatus{Conditions: conds},
	}
}

func TestIsAPIExportReady(t *testing.T) {
	t.Parallel()
	t.Run("ready when both conditions are True", func(t *testing.T) {
		t.Parallel()
		e := exportWithCreation("root:ws", "test", time.Now(),
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		require.True(t, isAPIExportReady(e))
	})

	t.Run("not ready when identity is False", func(t *testing.T) {
		t.Parallel()
		e := exportWithCreation("root:ws", "test", time.Now(),
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionFalse),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		require.False(t, isAPIExportReady(e))
	})

	t.Run("not ready when URLs not ready", func(t *testing.T) {
		t.Parallel()
		e := exportWithCreation("root:ws", "test", time.Now(),
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionFalse),
		)
		require.False(t, isAPIExportReady(e))
	})

	t.Run("not ready with no conditions", func(t *testing.T) {
		t.Parallel()
		e := exportWithCreation("root:ws", "test", time.Now())
		require.False(t, isAPIExportReady(e))
	})
}

func TestHandleReadyDurationMetricOnUpdate(t *testing.T) {
	t.Parallel()
	t.Run("first transition to ready records duration without panic", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-3 * time.Second)
		e := exportWithCreation("root:ws", "test", created,
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		require.NotPanics(t, func() { c.handleReadyDurationMetricOnUpdate(e) })
		require.Len(t, c.readyAPIExports, 1)
	})

	t.Run("second update when already ready does not double-record", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-3 * time.Second)
		e := exportWithCreation("root:ws", "test", created,
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		c.handleReadyDurationMetricOnUpdate(e)
		c.handleReadyDurationMetricOnUpdate(e) // second call should be a no-op
		require.Len(t, c.readyAPIExports, 1)
	})

	t.Run("not-ready update does not record", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithCreation("root:ws", "test", time.Now(),
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionFalse),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		c.handleReadyDurationMetricOnUpdate(e)
		require.Empty(t, c.readyAPIExports)
	})
}

func TestHandleReadyDurationMetricOnDelete(t *testing.T) {
	t.Parallel()
	t.Run("delete removes ready tracking", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-1 * time.Second)
		e := exportWithCreation("root:ws", "test", created,
			cond(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
			cond(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionTrue),
		)
		c.handleReadyDurationMetricOnUpdate(e)
		require.Len(t, c.readyAPIExports, 1)
		c.handleReadyDurationMetricOnDelete(e)
		require.Empty(t, c.readyAPIExports)
	})

	t.Run("delete of untracked export is a no-op", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		e := exportWithCreation("root:ws", "unknown", time.Now())
		require.NotPanics(t, func() { c.handleReadyDurationMetricOnDelete(e) })
	})
}
