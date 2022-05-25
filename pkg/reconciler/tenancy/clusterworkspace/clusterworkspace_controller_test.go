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
						"pluto", "venus", "apollo",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase":              "Ready",
					"internal.kcp.dev/initializer.pluto":  "",
					"internal.kcp.dev/initializer.venus":  "",
					"internal.kcp.dev/initializer.apollo": "",
				},
			},
		},
		{
			name: "adds missing partially labels",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase":              "Ready",
						"internal.kcp.dev/initializer.pluto":  "",
						"internal.kcp.dev/initializer.apollo": "",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"pluto", "venus", "apollo",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase":              "Ready",
					"internal.kcp.dev/initializer.pluto":  "",
					"internal.kcp.dev/initializer.venus":  "",
					"internal.kcp.dev/initializer.apollo": "",
				},
			},
		},
		{
			name: "removes previously-needed labels removed on mutation that removes initializer",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase":              "Ready",
						"internal.kcp.dev/initializer.pluto":  "",
						"internal.kcp.dev/initializer.apollo": "",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						"pluto",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"internal.kcp.dev/phase":             "Ready",
					"internal.kcp.dev/initializer.pluto": "",
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
