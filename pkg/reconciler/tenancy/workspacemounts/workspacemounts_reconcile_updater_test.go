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

package workspacemounts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestWorkspaceSpecUpdater(t *testing.T) {
	t.Parallel()

	mountObject := func(statusURL string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "contrib.kcp.io/v1alpha1",
			"kind":       "KubeCluster",
			"metadata":   map[string]interface{}{"name": "proxy-cluster"},
			"status":     map[string]interface{}{"URL": statusURL, "phase": "Ready"},
		}}
	}
	newWorkspace := func() *tenancyv1alpha1.Workspace {
		ws := &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "mount", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
			Spec: tenancyv1alpha1.WorkspaceSpec{
				URL:   "https://previous.example.com/clusters/root:dest",
				Mount: &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
			},
			Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
		}
		conditions.MarkTrue(ws, tenancyv1alpha1.MountConditionReady)
		return ws
	}

	t.Run("accepts an https URL", func(t *testing.T) {
		t.Parallel()
		r := &workspaceSpecUpdater{getMountObject: func(context.Context, logicalcluster.Name, tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error) {
			return mountObject("https://new.example.com/clusters/root:dest"), nil
		}}
		ws := newWorkspace()
		status, err := r.reconcile(t.Context(), ws)
		require.NoError(t, err)
		require.Equal(t, reconcileStatusContinue, status)
		require.Equal(t, "https://new.example.com/clusters/root:dest", ws.Spec.URL)
		require.Equal(t, corev1alpha1.LogicalClusterPhaseReady, ws.Status.Phase)
		require.True(t, conditions.IsTrue(ws, tenancyv1alpha1.MountConditionReady))
	})

	for _, invalid := range []string{
		"http://attacker.example.com",
		"https://user:token@attacker.example.com",
		"https://attacker.example.com/?x=y",
		"attacker.example.com",
		"",
	} {
		t.Run("rejects "+invalid, func(t *testing.T) {
			t.Parallel()
			r := &workspaceSpecUpdater{getMountObject: func(context.Context, logicalcluster.Name, tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error) {
				return mountObject(invalid), nil
			}}
			ws := newWorkspace()
			status, err := r.reconcile(t.Context(), ws)
			require.NoError(t, err)
			require.Equal(t, reconcileStatusContinue, status)
			require.Equal(t, "https://previous.example.com/clusters/root:dest", ws.Spec.URL, "an invalid mount URL must not be copied into the workspace")
		})
	}
}

func TestWorkspaceStatusUpdater(t *testing.T) {
	t.Parallel()

	mountObject := func(statusURL, phase string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "contrib.kcp.io/v1alpha1",
			"kind":       "KubeCluster",
			"metadata":   map[string]interface{}{"name": "proxy-cluster"},
			"status":     map[string]interface{}{"URL": statusURL, "phase": phase},
		}}
	}
	newWorkspace := func(phase corev1alpha1.LogicalClusterPhaseType) *tenancyv1alpha1.Workspace {
		return &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "mount", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
			Spec: tenancyv1alpha1.WorkspaceSpec{
				Mount: &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
			},
			Status: tenancyv1alpha1.WorkspaceStatus{Phase: phase},
		}
	}

	t.Run("ready mount with https URL becomes Ready", func(t *testing.T) {
		t.Parallel()
		r := &workspaceStatusUpdater{getMountObject: func(context.Context, logicalcluster.Name, tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error) {
			return mountObject("https://new.example.com/clusters/root:dest", "Ready"), nil
		}}
		ws := newWorkspace(corev1alpha1.LogicalClusterPhaseUnavailable)
		status, err := r.reconcile(t.Context(), ws)
		require.NoError(t, err)
		require.Equal(t, reconcileStatusContinue, status)
		require.Equal(t, corev1alpha1.LogicalClusterPhaseReady, ws.Status.Phase)
		require.True(t, conditions.IsTrue(ws, tenancyv1alpha1.MountConditionReady))
	})

	for _, tc := range []struct {
		name      string
		phase     corev1alpha1.LogicalClusterPhaseType
		wantPhase corev1alpha1.LogicalClusterPhaseType
	}{
		{name: "ready mount with http URL takes a Ready workspace to Unavailable", phase: corev1alpha1.LogicalClusterPhaseReady, wantPhase: corev1alpha1.LogicalClusterPhaseUnavailable},
		{name: "ready mount with http URL keeps a non-Ready workspace out of Ready", phase: corev1alpha1.LogicalClusterPhaseScheduling, wantPhase: corev1alpha1.LogicalClusterPhaseScheduling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &workspaceStatusUpdater{getMountObject: func(context.Context, logicalcluster.Name, tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error) {
				return mountObject("http://attacker.example.com", "Ready"), nil
			}}
			ws := newWorkspace(tc.phase)
			status, err := r.reconcile(t.Context(), ws)
			require.NoError(t, err)
			require.Equal(t, reconcileStatusContinue, status)
			require.Equal(t, tc.wantPhase, ws.Status.Phase)
			cond := conditions.Get(ws, tenancyv1alpha1.MountConditionReady)
			require.NotNil(t, cond)
			require.Equal(t, corev1.ConditionFalse, cond.Status)
			require.Equal(t, tenancyv1alpha1.MountObjectInvalidURLReason, cond.Reason)
		})
	}
}
