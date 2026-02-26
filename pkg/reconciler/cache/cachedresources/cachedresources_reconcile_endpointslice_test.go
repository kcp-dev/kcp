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

package cachedresources

import (
	"context"
	goerrors "errors"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

func TestReconcileCachedResourceEndpointSlice(t *testing.T) {
	var createEndpointSliceCalled bool
	tests := []struct {
		endpointSliceReconciler endpointSlice
		cachedResource          cachev1alpha1.CachedResource

		shouldCallCreateEndpointSlice bool
		shouldFail                    bool
	}{
		// Getting the CachedResourceEndpointSlice fails and so the reconiliation should fail too.
		{
			endpointSliceReconciler: endpointSlice{
				getEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return nil, goerrors.New("")
				},
			},
			cachedResource: cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster",
					},
				},
			},
			shouldCallCreateEndpointSlice: false,
			shouldFail:                    true,
		},
		// CachedResourceEndpointSlice doesn't exist, and it should not be created
		// because of the CachedResourceEndpointSliceSkipAnnotation annotation.
		{
			endpointSliceReconciler: endpointSlice{
				getEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return nil, errors.NewNotFound(cachev1alpha1.Resource("cachedresourceendpointslices"), name)
				},
			},
			cachedResource: cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:                            "cluster",
						cachev1alpha1.CachedResourceEndpointSliceSkipAnnotation: "",
					},
				},
			},
			shouldCallCreateEndpointSlice: false,
			shouldFail:                    false,
		},
		// CachedResourceEndpointSlice doesn't exist, and it should be created.
		{
			endpointSliceReconciler: endpointSlice{
				getEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return nil, errors.NewNotFound(cachev1alpha1.Resource("cachedresourceendpointslices"), name)
				},
				createEndpointSlice: func(ctx context.Context, path logicalcluster.Path, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice) error {
					createEndpointSliceCalled = true
					return nil
				},
			},

			cachedResource: cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster",
					},
				},
			},
			shouldCallCreateEndpointSlice: true,
			shouldFail:                    false,
		},
		// CachedResourceEndpointSlice already exists.
		{
			endpointSliceReconciler: endpointSlice{
				getEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{}, nil
				},
				createEndpointSlice: func(ctx context.Context, path logicalcluster.Path, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice) error {
					createEndpointSliceCalled = true
					return nil
				},
			},

			cachedResource: cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster",
					},
				},
			},
			shouldCallCreateEndpointSlice: false,
			shouldFail:                    false,
		},
	}

	for i, testCase := range tests {
		createEndpointSliceCalled = false
		_, err := testCase.endpointSliceReconciler.reconcile(context.Background(), &testCase.cachedResource)
		if testCase.shouldFail {
			require.Error(t, err, "expected reconcile to fail", "case", i)
		} else {
			require.NoError(t, err, "expected reconcile to succeed", "case", i)
		}
		require.Equal(t, testCase.shouldCallCreateEndpointSlice, createEndpointSliceCalled, "expected to try to create CachedResourceEndpointSlice", "case", i)
	}
}
