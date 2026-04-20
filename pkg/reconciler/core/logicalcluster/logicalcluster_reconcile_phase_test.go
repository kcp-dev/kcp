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

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestPhaseReconcile(t *testing.T) {
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
