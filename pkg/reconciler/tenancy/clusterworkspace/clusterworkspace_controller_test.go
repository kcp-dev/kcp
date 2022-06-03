/*
Copyright 2022 The KCP Authors.

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

package clusterworkspace

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestReconcileMetadata(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		input    *tenancyv1alpha1.ClusterWorkspace
		expected metav1.ObjectMeta
	}{
		{
			name: "adds entirely missing labels",
			input: &tenancyv1alpha1.ClusterWorkspace{
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						{Name: "pluto", Path: "root:org:ws"},
						{Name: "venus", Path: "root:org:ws"},
						{Name: "apollo", Path: "root:org:ws"},
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2cf63e1fc0d673be2a7c7e394a17426b45": "2cf63e1fc0d673be2a7c7e394a17426b45735b89b6c14fa5684541c9",
					"initializer.internal.kcp.dev/384afe078ecbaec83f15760c079c1becb7": "384afe078ecbaec83f15760c079c1becb7a675d339e1dc87429ca4f9",
					"initializer.internal.kcp.dev/97d3317200419270f9e121d5a2db4e2d14": "97d3317200419270f9e121d5a2db4e2d14c1c7508d4e2b48bdd7dec4",
				},
			},
		},
		{
			name: "adds missing partially labels",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase": "Ready",
						"initializer.internal.kcp.dev/2cf63e1fc0d673be2a7c7e394a17426b45": "2cf63e1fc0d673be2a7c7e394a17426b45735b89b6c14fa5684541c9",
						"initializer.internal.kcp.dev/97d3317200419270f9e121d5a2db4e2d14": "97d3317200419270f9e121d5a2db4e2d14c1c7508d4e2b48bdd7dec4",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						{Name: "pluto", Path: "root:org:ws"},
						{Name: "venus", Path: "root:org:ws"},
						{Name: "apollo", Path: "root:org:ws"},
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2cf63e1fc0d673be2a7c7e394a17426b45": "2cf63e1fc0d673be2a7c7e394a17426b45735b89b6c14fa5684541c9",
					"initializer.internal.kcp.dev/384afe078ecbaec83f15760c079c1becb7": "384afe078ecbaec83f15760c079c1becb7a675d339e1dc87429ca4f9",
					"initializer.internal.kcp.dev/97d3317200419270f9e121d5a2db4e2d14": "97d3317200419270f9e121d5a2db4e2d14c1c7508d4e2b48bdd7dec4",
				},
			},
		},
		{
			name: "removes previously-needed labels removed on mutation that removes initializer",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase": "Ready",
						"initializer.internal.kcp.dev/2cf63e1fc0d673be2a7c7e394a17426b45": "2cf63e1fc0d673be2a7c7e394a17426b45735b89b6c14fa5684541c9",
						"initializer.internal.kcp.dev/384afe078ecbaec83f15760c079c1becb7": "384afe078ecbaec83f15760c079c1becb7a675d339e1dc87429ca4f9",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						{Name: "pluto", Path: "root:org:ws"},
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2cf63e1fc0d673be2a7c7e394a17426b45": "2cf63e1fc0d673be2a7c7e394a17426b45735b89b6c14fa5684541c9",
				},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reconcileMetadata(testCase.input)
			if diff := cmp.Diff(testCase.input.ObjectMeta, testCase.expected); diff != "" {
				t.Errorf("invalid output after reconciling metadata: %v", diff)
			}
		})
	}
}
