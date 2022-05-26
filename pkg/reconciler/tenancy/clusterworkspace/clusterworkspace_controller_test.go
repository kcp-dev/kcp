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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestClusterWorkspaceInitializerLabelPrefix(t *testing.T) {
	// we want to be able to add this prefix to any valid initializer and have it
	// end up as a valid label, so it needs to be a DNS 1123 subdomain
	if errs := validation.IsDNS1123Subdomain(tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix + "a"); len(errs) > 0 {
		t.Errorf("tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix invalid: %s", strings.Join(errs, ", "))
	}
}

func TestInitializerLabelFor(t *testing.T) {
	for _, testCase := range []tenancyv1alpha1.ClusterWorkspaceInitializer{
		"simple",
		"QualifiedName",
		"qualified.Name",
		"qualified-123-name",
		"with.dns/prefix",
		"with.dns/prefix_and.Qualified-name",
	} {
		label := initializerLabelFor(testCase)
		if errs := validation.IsQualifiedName(label); len(errs) > 0 {
			t.Errorf("initializer %q produces an invalid label %q: %s", testCase, label, strings.Join(errs, ", "))
		}
	}
}

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
					"initializer.internal.kcp.dev-pluto":  "",
					"initializer.internal.kcp.dev-venus":  "",
					"initializer.internal.kcp.dev-apollo": "",
				},
			},
		},
		{
			name: "adds missing partially labels",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase":              "Ready",
						"initializer.internal.kcp.dev-pluto":  "",
						"initializer.internal.kcp.dev-apollo": "",
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
					"initializer.internal.kcp.dev-pluto":  "",
					"initializer.internal.kcp.dev-venus":  "",
					"initializer.internal.kcp.dev-apollo": "",
				},
			},
		},
		{
			name: "removes previously-needed labels removed on mutation that removes initializer",
			input: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"internal.kcp.dev/phase":              "Ready",
						"initializer.internal.kcp.dev-pluto":  "",
						"initializer.internal.kcp.dev-apollo": "",
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
					"initializer.internal.kcp.dev-pluto": "",
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
