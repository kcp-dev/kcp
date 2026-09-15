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

package logicalcluster

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/contextmanager"
)

func TestPhaseReconcile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		logicalCluster *corev1alpha1.LogicalCluster
		expectedPhase  corev1alpha1.LogicalClusterPhaseType
	}{
		{
			name: "scheduling advances to initializing and stops so admission can populate initializers",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseInitializing,
		},
		{
			name: "scheduling with initializers advances to initializing",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseScheduling,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"init"},
				},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseInitializing,
		},
		{
			name: "empty phase advances to initializing",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseInitializing,
		},
		{
			name: "initializing without initializers advances to ready",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseReady,
		},
		{
			name: "initializing with initializers stays initializing",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"init"},
				},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseInitializing,
		},
		{
			name: "ready stays ready",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				},
			},
			expectedPhase: corev1alpha1.LogicalClusterPhaseReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &phaseReconciler{}
			if _, err := r.reconcile(context.Background(), tt.logicalCluster); err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}
			if got := tt.logicalCluster.Status.Phase; got != tt.expectedPhase {
				t.Errorf("phase: got %q, want %q", got, tt.expectedPhase)
			}
		})
	}
}

func newLogicalClusterInPhase(clusterName string, phase corev1alpha1.LogicalClusterPhaseType, inactive bool) *corev1alpha1.LogicalCluster {
	lc := &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
		},
		Status: corev1alpha1.LogicalClusterStatus{
			Phase: phase,
		},
	}
	if inactive {
		lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey] = "true"
	}
	return lc
}

func newContext(t *testing.T, mgr *contextmanager.Manager[logicalcluster.Path], key logicalcluster.Path) context.Context {
	t.Helper()
	ctx, cleanup := mgr.Context(context.Background(), key)
	t.Cleanup(cleanup)
	return ctx
}

// requireDone waits for ctx to be cancelled. The context manager propagates
// cancellation to derived contexts asynchronously.
func requireDone(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(wait.ForeverTestTimeout):
		t.Error(msg)
	}
}

func requireNotDone(t *testing.T, ctx context.Context, msg string) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Error(msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func reconcilePhase(t *testing.T, r *phaseReconciler, lc *corev1alpha1.LogicalCluster) {
	t.Helper()
	if _, err := r.reconcile(context.Background(), lc); err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
}

func TestPhaseReconcileInactive(t *testing.T) {
	t.Parallel()

	const clusterName = "test"
	lcPath := logicalcluster.NewPath(clusterName)

	t.Run("marking inactive does not cancel before the Inactive phase is persisted", func(t *testing.T) {
		t.Parallel()
		mgr := contextmanager.New[logicalcluster.Path](context.Background())
		r := &phaseReconciler{clusterContextManager: mgr}
		lcCtx := newContext(t, mgr, lcPath)
		wildcardCtx := newContext(t, mgr, logicalcluster.Wildcard)

		lc := newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseReady, true)
		reconcilePhase(t, r, lc)

		if got := lc.Status.Phase; got != corev1alpha1.LogicalClusterPhaseInactive {
			t.Errorf("phase: got %q, want %q", got, corev1alpha1.LogicalClusterPhaseInactive)
		}
		if mgr.IsCancelled(lcPath) {
			t.Errorf("logical cluster context cancelled before the Inactive phase was persisted")
		}
		requireNotDone(t, lcCtx, "logical cluster connection terminated before the Inactive phase was persisted")
		requireNotDone(t, wildcardCtx, "wildcard connection terminated before the Inactive phase was persisted")
	})

	t.Run("annotation removed before the Inactive phase is persisted leaves contexts live", func(t *testing.T) {
		t.Parallel()
		mgr := contextmanager.New[logicalcluster.Path](context.Background())
		r := &phaseReconciler{clusterContextManager: mgr}

		// First reconcile wants to move to Inactive, but the status update
		// conflicts because the annotation was removed in the meantime.
		reconcilePhase(t, r, newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseReady, true))
		// The requeue observes the logical cluster in Ready without the annotation.
		lc := newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseReady, false)
		reconcilePhase(t, r, lc)

		if got := lc.Status.Phase; got != corev1alpha1.LogicalClusterPhaseReady {
			t.Errorf("phase: got %q, want %q", got, corev1alpha1.LogicalClusterPhaseReady)
		}
		if mgr.IsCancelled(lcPath) {
			t.Errorf("logical cluster context is cancelled")
		}
		if mgr.IsCancelled(logicalcluster.Wildcard) {
			t.Errorf("wildcard context is cancelled")
		}
	})

	t.Run("inactive cancels the logical cluster and terminates wildcard connections once", func(t *testing.T) {
		t.Parallel()
		mgr := contextmanager.New[logicalcluster.Path](context.Background())
		r := &phaseReconciler{clusterContextManager: mgr}
		lcCtx := newContext(t, mgr, lcPath)
		wildcardCtx := newContext(t, mgr, logicalcluster.Wildcard)

		lc := newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseInactive, true)
		reconcilePhase(t, r, lc)

		if got := lc.Status.Phase; got != corev1alpha1.LogicalClusterPhaseInactive {
			t.Errorf("phase: got %q, want %q", got, corev1alpha1.LogicalClusterPhaseInactive)
		}
		if !mgr.IsCancelled(lcPath) {
			t.Errorf("logical cluster context is live while the logical cluster is inactive")
		}
		requireDone(t, lcCtx, "existing logical cluster connection was not terminated")
		requireDone(t, wildcardCtx, "existing wildcard connection was not terminated")

		// New wildcard requests must not be blocked while the logical
		// cluster is inactive, and further reconciles must not terminate
		// them again.
		if mgr.IsCancelled(logicalcluster.Wildcard) {
			t.Errorf("wildcard context is cancelled while the logical cluster is inactive")
		}
		newWildcardCtx := newContext(t, mgr, logicalcluster.Wildcard)
		reconcilePhase(t, r, newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseInactive, true))
		requireNotDone(t, newWildcardCtx, "wildcard connection was terminated again by a subsequent reconcile")
	})

	t.Run("reactivation drops the cancelled logical cluster context", func(t *testing.T) {
		t.Parallel()
		mgr := contextmanager.New[logicalcluster.Path](context.Background())
		r := &phaseReconciler{clusterContextManager: mgr}

		reconcilePhase(t, r, newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseInactive, true))
		if !mgr.IsCancelled(lcPath) {
			t.Fatalf("logical cluster context is live while the logical cluster is inactive")
		}

		lc := newLogicalClusterInPhase(clusterName, corev1alpha1.LogicalClusterPhaseInactive, false)
		reconcilePhase(t, r, lc)

		if got := lc.Status.Phase; got != corev1alpha1.LogicalClusterPhaseReady {
			t.Errorf("phase: got %q, want %q", got, corev1alpha1.LogicalClusterPhaseReady)
		}
		if mgr.IsCancelled(lcPath) {
			t.Errorf("logical cluster context is cancelled after reactivation")
		}
		if mgr.IsCancelled(logicalcluster.Wildcard) {
			t.Errorf("wildcard context is cancelled after reactivation")
		}
	})
}
