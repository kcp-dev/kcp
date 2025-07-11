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

package cachedresourceendpointslice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestEndpointsReconciler(t *testing.T) {
	tests := map[string]struct {
		r           endpointsReconciler
		in, out     cachev1alpha1.CachedResourceEndpointSlice
		wantsErr    error
		wantsStatus reconcileStatus
	}{
		"ShouldAdd": {
			r: endpointsReconciler{
				getCachedResource: func(logicalcluster.Name, string) (*cachev1alpha1.CachedResource, error) {
					return &cachev1alpha1.CachedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-cachedresource",
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "my-workspace",
							},
						},
					}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-shard",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://my-shard.kcp.dev",
						},
					}, nil
				},
			},
			in: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
			},
			out: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
				Status: cachev1alpha1.CachedResourceEndpointSliceStatus{
					CachedResourceEndpoints: []cachev1alpha1.CachedResourceEndpoint{
						{
							URL: "https://my-shard.kcp.dev/services/replication/my-workspace/my-cachedresource"},
					},
				},
			},
			wantsStatus: reconcileStatusContinue,
		},
		"ShouldNotAddBecauseAlreadyPresent": {
			r: endpointsReconciler{
				getCachedResource: func(logicalcluster.Name, string) (*cachev1alpha1.CachedResource, error) {
					return &cachev1alpha1.CachedResource{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-cachedresource",
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "my-workspace",
							},
						},
					}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-shard",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://my-shard.kcp.dev",
						},
					}, nil
				},
			},
			in: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
				Status: cachev1alpha1.CachedResourceEndpointSliceStatus{
					CachedResourceEndpoints: []cachev1alpha1.CachedResourceEndpoint{
						{
							URL: "https://my-shard.kcp.dev/services/replication/my-workspace/my-cachedresource"},
					},
				},
			},
			out: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
				Status: cachev1alpha1.CachedResourceEndpointSliceStatus{
					CachedResourceEndpoints: []cachev1alpha1.CachedResourceEndpoint{
						{
							URL: "https://my-shard.kcp.dev/services/replication/my-workspace/my-cachedresource"},
					},
				},
			},
			wantsStatus: reconcileStatusContinue,
		},
		"ShouldRemove": {
			r: endpointsReconciler{
				getCachedResource: func(logicalcluster.Name, string) (*cachev1alpha1.CachedResource, error) {
					return nil, apierrors.NewNotFound(cachev1alpha1.Resource("cachedresources"), "my-cachedresource")
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-shard",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://my-shard.kcp.dev",
						},
					}, nil
				},
			},
			in: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
				Status: cachev1alpha1.CachedResourceEndpointSliceStatus{
					CachedResourceEndpoints: []cachev1alpha1.CachedResourceEndpoint{
						{
							URL: "https://my-shard.kcp.dev/services/replication/my-workspace/my-cachedresource"},
					},
				},
			},
			out: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
				Status: cachev1alpha1.CachedResourceEndpointSliceStatus{
					CachedResourceEndpoints: []cachev1alpha1.CachedResourceEndpoint{},
				},
			},
			wantsStatus: reconcileStatusContinue,
		},
		"ShouldSkipBecauseNotFound": {
			r: endpointsReconciler{
				getCachedResource: func(logicalcluster.Name, string) (*cachev1alpha1.CachedResource, error) {
					return nil, apierrors.NewNotFound(cachev1alpha1.Resource("cachedresources"), "my-cachedresource")
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-shard",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://my-shard.kcp.dev",
						},
					}, nil
				},
			},
			in: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
			},
			out: cachev1alpha1.CachedResourceEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-workspace",
					},
				},
				Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
					CachedResource: cachev1alpha1.CachedResourceReference{
						Name: "my-cachedresource",
					},
				},
			},
			wantsStatus: reconcileStatusContinue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			status, err := tc.r.reconcile(context.Background(), &tc.in)
			require.Equal(t, tc.wantsStatus, status, "unexpected reconcile status")
			require.Equal(t, tc.wantsErr, err, "unexpected error value")
			require.Equal(t, tc.out, tc.in)
		})
	}
}
