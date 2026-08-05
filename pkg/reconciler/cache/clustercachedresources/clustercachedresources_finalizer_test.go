/*
Copyright 2025 The kcp Authors.

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

package clustercachedresources

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

func TestFinalizer_Reconcile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                  string
		ClusterCachedResource *cachev1alpha1.ClusterCachedResource
		expectedStatus        reconcileStatus
		expectedFinalizers    []string
		expectedPhase         cachev1alpha1.ClusterCachedResourcePhaseType
	}{
		{
			name: "case 1: remove finalizer when resource is deleted and in Deleted phase",
			ClusterCachedResource: &cachev1alpha1.ClusterCachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.ClusterCachedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.ClusterCachedResourceStatus{
					Phase: cachev1alpha1.ClusterCachedResourcePhaseDeleted,
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{},
			expectedPhase:      cachev1alpha1.ClusterCachedResourcePhaseDeleted,
		},
		{
			name: "case 2: mark as deleting when resource is deleted but not in Deleting phase",
			ClusterCachedResource: &cachev1alpha1.ClusterCachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.ClusterCachedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.ClusterCachedResourceStatus{
					Phase: cachev1alpha1.ClusterCachedResourcePhaseReady,
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{cachev1alpha1.ClusterCachedResourceFinalizer},
			expectedPhase:      cachev1alpha1.ClusterCachedResourcePhaseDeleting,
		},
		{
			name: "case 2: no change when resource is already in Deleting phase",
			ClusterCachedResource: &cachev1alpha1.ClusterCachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.ClusterCachedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.ClusterCachedResourceStatus{
					Phase: cachev1alpha1.ClusterCachedResourcePhaseDeleting,
				},
			},
			expectedStatus:     reconcileStatusContinue,
			expectedFinalizers: []string{cachev1alpha1.ClusterCachedResourceFinalizer},
			expectedPhase:      cachev1alpha1.ClusterCachedResourcePhaseDeleting,
		},
		{
			name: "case 3: add finalizer when resource is not deleted",
			ClusterCachedResource: &cachev1alpha1.ClusterCachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{cachev1alpha1.ClusterCachedResourceFinalizer},
			expectedPhase:      "",
		},
		{
			name: "case 3: keep finalizer when resource is not deleted and finalizer present",
			ClusterCachedResource: &cachev1alpha1.ClusterCachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test",
					Finalizers: []string{cachev1alpha1.ClusterCachedResourceFinalizer},
				},
			},
			expectedStatus:     reconcileStatusContinue,
			expectedFinalizers: []string{cachev1alpha1.ClusterCachedResourceFinalizer},
			expectedPhase:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			finalizer := &finalizer{}
			status, err := finalizer.reconcile(context.Background(), tt.ClusterCachedResource)
			require.NoError(t, err)
			require.Equal(t, tt.expectedStatus, status)
			require.Equal(t, tt.expectedFinalizers, tt.ClusterCachedResource.Finalizers)
			require.Equal(t, tt.expectedPhase, tt.ClusterCachedResource.Status.Phase)
		})
	}
}
