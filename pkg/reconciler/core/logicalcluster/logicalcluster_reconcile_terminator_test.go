/*
Copyright 2025 The KCP Authors.

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

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestReconcile(t *testing.T) {
	tests := []struct {
		name               string
		logicalCluster     *corev1alpha1.LogicalCluster
		expectedFinalizers []string
	}{
		{
			name: "remove finalizer if there are no terminators",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{},
				},
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{LogicalClusterHasTerminatorFinalizer},
				},
			},
			expectedFinalizers: []string{},
		},
		{
			name: "has terminators, finalizer added",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{"terminator1"},
				},
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{},
				},
			},
			expectedFinalizers: []string{LogicalClusterHasTerminatorFinalizer},
		},
		{
			name: "do nothing if finalizers already present and terminators exist",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{"terminator1"},
				},
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{LogicalClusterHasTerminatorFinalizer},
				},
			},
			expectedFinalizers: []string{LogicalClusterHasTerminatorFinalizer},
		},
		{
			name: "do nothing if finalizers already absent and no terminators exist",
			logicalCluster: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Terminators: []corev1alpha1.LogicalClusterTerminator{},
				},
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{},
				},
			},
			expectedFinalizers: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &terminatorReconciler{}
			_, err := reconciler.reconcile(context.Background(), tt.logicalCluster)
			if err != nil {
				t.Errorf("unexpected reconcile error %v", err)
			}
			if diff := cmp.Diff(tt.expectedFinalizers, tt.logicalCluster.Finalizers); diff != "" {
				t.Errorf("unexpected finalizer diff:\n%s", diff)
			}
		})
	}
}
