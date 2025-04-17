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

package publishedresources

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
)

func TestFinalizer_Reconcile(t *testing.T) {
	tests := []struct {
		name               string
		publishedResource  *cachev1alpha1.PublishedResource
		expectedStatus     reconcileStatus
		expectedFinalizers []string
		expectedPhase      cachev1alpha1.PublishedResourcePhaseType
	}{
		{
			name: "case 1: remove finalizer when resource is deleted and in Deleted phase",
			publishedResource: &cachev1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.PublishedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.PublishedResourceStatus{
					Phase: cachev1alpha1.PublishedResourcePhaseDeleted,
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{},
			expectedPhase:      cachev1alpha1.PublishedResourcePhaseDeleted,
		},
		{
			name: "case 2: mark as deleting when resource is deleted but not in Deleting phase",
			publishedResource: &cachev1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.PublishedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.PublishedResourceStatus{
					Phase: cachev1alpha1.PublishedResourcePhaseReady,
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{cachev1alpha1.PublishedResourceFinalizer},
			expectedPhase:      cachev1alpha1.PublishedResourcePhaseDeleting,
		},
		{
			name: "case 2: no change when resource is already in Deleting phase",
			publishedResource: &cachev1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Finalizers:        []string{cachev1alpha1.PublishedResourceFinalizer},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: cachev1alpha1.PublishedResourceStatus{
					Phase: cachev1alpha1.PublishedResourcePhaseDeleting,
				},
			},
			expectedStatus:     reconcileStatusContinue,
			expectedFinalizers: []string{cachev1alpha1.PublishedResourceFinalizer},
			expectedPhase:      cachev1alpha1.PublishedResourcePhaseDeleting,
		},
		{
			name: "case 3: add finalizer when resource is not deleted",
			publishedResource: &cachev1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedFinalizers: []string{cachev1alpha1.PublishedResourceFinalizer},
			expectedPhase:      "",
		},
		{
			name: "case 3: keep finalizer when resource is not deleted and finalizer present",
			publishedResource: &cachev1alpha1.PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test",
					Finalizers: []string{cachev1alpha1.PublishedResourceFinalizer},
				},
			},
			expectedStatus:     reconcileStatusContinue,
			expectedFinalizers: []string{cachev1alpha1.PublishedResourceFinalizer},
			expectedPhase:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finalizer := &finalizer{}
			status, err := finalizer.reconcile(context.Background(), tt.publishedResource)
			require.NoError(t, err)
			require.Equal(t, tt.expectedStatus, status)
			require.Equal(t, tt.expectedFinalizers, tt.publishedResource.Finalizers)
			require.Equal(t, tt.expectedPhase, tt.publishedResource.Status.Phase)
		})
	}
}
