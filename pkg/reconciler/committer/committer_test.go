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

package committer

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestGeneratePatchAndSubResourcesMultipleSubresourcesPanic(t *testing.T) {
	// if we change status + either spec or objectmeta, we expect a panic
	tt := []struct {
		name string
		old  *Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]
		new  *Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]
	}{
		{
			name: "spec and status changed",
			old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
				Spec:   corev1alpha1.LogicalClusterSpec{DirectlyDeletable: true},
				Status: corev1alpha1.LogicalClusterStatus{Phase: "old"},
			},
			new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
				Spec:   corev1alpha1.LogicalClusterSpec{DirectlyDeletable: false},
				Status: corev1alpha1.LogicalClusterStatus{Phase: "new"},
			},
		},
		{
			name: "meta and status changed",
			old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
				ObjectMeta: metav1.ObjectMeta{Finalizers: []string{}},
				Status:     corev1alpha1.LogicalClusterStatus{Phase: "old"},
			},
			new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
				ObjectMeta: metav1.ObjectMeta{Finalizers: []string{"changed"}},
				Status:     corev1alpha1.LogicalClusterStatus{Phase: "new"},
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			require.Panics(t, func() {
				_, _, _ = generatePatchAndSubResources(tc.old, tc.new)
			})
		})
	}
}

func TestGeneratePatchAndSubResources(t *testing.T) {
	{
		tt := []struct {
			name                 string
			old                  *Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]
			new                  *Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]
			expectedPatch        string
			expectedSubResources []string
		}{
			{
				name: "spec changed",
				old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					Spec: corev1alpha1.LogicalClusterSpec{DirectlyDeletable: true},
				},
				new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					Spec: corev1alpha1.LogicalClusterSpec{DirectlyDeletable: false},
				},
				expectedPatch: `{"spec":{"directlyDeletable":null}}`,
				// for spec changes, we expect no subresources
				expectedSubResources: nil,
			},
			{
				name: "status changed",
				old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					Status: corev1alpha1.LogicalClusterStatus{Phase: "old"},
				},
				new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					Status: corev1alpha1.LogicalClusterStatus{Phase: "new"},
				},
				expectedPatch:        `{"status":{"phase":"new"}}`,
				expectedSubResources: []string{"status"},
			},
			{
				name: "meta changed",
				old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					ObjectMeta: metav1.ObjectMeta{Finalizers: []string{}},
				},
				new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					ObjectMeta: metav1.ObjectMeta{Finalizers: []string{"changed"}},
				},
				expectedPatch: `{"metadata":{"finalizers":["changed"]}}`,
				// for meta changes, we expect no subresources
				expectedSubResources: nil,
			},
			{
				name: "no changes",
				old: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					ObjectMeta: metav1.ObjectMeta{Finalizers: []string{}},
				},
				new: &Resource[corev1alpha1.LogicalClusterSpec, corev1alpha1.LogicalClusterStatus]{
					ObjectMeta: metav1.ObjectMeta{Finalizers: []string{}},
				},
				expectedPatch:        "",
				expectedSubResources: nil,
			},
		}

		for _, tc := range tt {
			t.Run(tc.name, func(t *testing.T) {
				patch, subresources, err := generatePatchAndSubResources(tc.old, tc.new)
				require.NoError(t, err)
				require.Equal(t, tc.expectedPatch, string(patch))
				require.Equal(t, tc.expectedSubResources, subresources)
			})
		}
	}
}
