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

package helpers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGroupObjectsByHierarchy(t *testing.T) {
	crd := newUnstructured("apiextensions.k8s.io/v1", "CustomResourceDefinition", "test-crd")
	crd2 := newUnstructured("apiextensions.k8s.io/v1", "CustomResourceDefinition", "test-crd2")
	apiExport := newUnstructured("apis.kcp.io/v1alpha1", "APIExport", "test-export")
	apiBinding := newUnstructured("apis.kcp.io/v1alpha1", "APIBinding", "test-binding")
	apiBinding2 := newUnstructured("apis.kcp.io/v1alpha1", "APIBinding", "test-binding2")
	namespace := newUnstructured("v1", "Namespace", "test-ns")
	configMap := newUnstructured("v1", "ConfigMap", "test-cm")
	configMap2 := newUnstructured("v1", "ConfigMap", "test-cm2")
	deployment := newUnstructured("apps/v1", "Deployment", "test-deploy")
	service := newUnstructured("v1", "Service", "test-svc")
	service2 := newUnstructured("v1", "Service", "test-svc2")

	testcases := []struct {
		name     string
		input    []*unstructured.Unstructured
		expected [][]*unstructured.Unstructured
	}{
		{
			name:     "empty input",
			input:    []*unstructured.Unstructured{},
			expected: [][]*unstructured.Unstructured{},
		},
		{
			name:  "single object",
			input: []*unstructured.Unstructured{configMap},
			expected: [][]*unstructured.Unstructured{
				{configMap},
			},
		},
		{
			name:  "multiple objects of same kind",
			input: []*unstructured.Unstructured{configMap, configMap2},
			expected: [][]*unstructured.Unstructured{
				{configMap, configMap2},
			},
		},
		{
			name:  "multiple CRDs only",
			input: []*unstructured.Unstructured{crd, crd2},
			expected: [][]*unstructured.Unstructured{
				{crd, crd2},
			},
		},
		{
			name:  "CRDs and APIExports",
			input: []*unstructured.Unstructured{apiExport, crd},
			expected: [][]*unstructured.Unstructured{
				{crd},
				{apiExport},
			},
		},
		{
			name:  "all hierarchy levels",
			input: []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace},
			expected: [][]*unstructured.Unstructured{
				{crd},
				{apiExport},
				{apiBinding},
				{namespace},
			},
		},
		{
			name:  "all hierarchy levels with multiple objects per level",
			input: []*unstructured.Unstructured{crd, crd2, apiExport, apiBinding, apiBinding2, namespace},
			expected: [][]*unstructured.Unstructured{
				{crd, crd2},
				{apiExport},
				{apiBinding, apiBinding2},
				{namespace},
			},
		},
		{
			name:  "mixed order with regular objects",
			input: []*unstructured.Unstructured{configMap, namespace, crd, deployment, service},
			expected: [][]*unstructured.Unstructured{
				{crd},
				{namespace},
				{configMap, deployment, service},
			},
		},
		{
			name:  "reverse order with all types",
			input: []*unstructured.Unstructured{service, deployment, configMap, namespace, apiBinding, apiExport, crd},
			expected: [][]*unstructured.Unstructured{
				{crd},
				{apiExport},
				{apiBinding},
				{namespace},
				{service, deployment, configMap},
			},
		},
		{
			name:  "multiple regular objects of different kinds",
			input: []*unstructured.Unstructured{service, service2, configMap, configMap2, deployment},
			expected: [][]*unstructured.Unstructured{
				{service, service2, configMap, configMap2, deployment},
			},
		},
		{
			name:  "complex mixed scenario",
			input: []*unstructured.Unstructured{deployment, configMap2, apiBinding2, service, crd2, namespace, apiExport, configMap, crd, service2, apiBinding},
			expected: [][]*unstructured.Unstructured{
				{crd2, crd},
				{apiExport},
				{apiBinding2, apiBinding},
				{namespace},
				{deployment, configMap2, service, configMap, service2},
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			groups := GroupObjectsByDefaultHierarchy(tt.input)

			assert.Len(t, groups, len(tt.expected), "Expected %d groups, got %d", len(tt.expected), len(groups))
			for i, group := range groups {
				assert.Len(t, group, len(tt.expected[i]), "Group %d: expected %d objects, got %d", i, len(tt.expected[i]), len(group))
				assert.ElementsMatch(t, tt.expected[i], group, "Group %d: unexpected objects in group", i)
			}
		})
	}
}

func TestSortObjectsByDefaultHierarchy(t *testing.T) {
	crd := newUnstructured("apiextensions.k8s.io/v1", "CustomResourceDefinition", "test-crd")
	apiExport := newUnstructured("apis.kcp.io/v1alpha1", "APIExport", "test-export")
	apiBinding := newUnstructured("apis.kcp.io/v1alpha1", "APIBinding", "test-binding")
	namespace := newUnstructured("v1", "Namespace", "test-ns")
	configMap := newUnstructured("v1", "ConfigMap", "test-cm")
	deployment := newUnstructured("apps/v1", "Deployment", "test-deploy")
	service := newUnstructured("v1", "Service", "test-svc")

	testcases := []struct {
		name     string
		input    []*unstructured.Unstructured
		expected []*unstructured.Unstructured
	}{
		{
			name:     "empty input",
			input:    []*unstructured.Unstructured{},
			expected: []*unstructured.Unstructured{},
		},
		{
			name:     "single object",
			input:    []*unstructured.Unstructured{configMap},
			expected: []*unstructured.Unstructured{configMap},
		},
		{
			name:     "already sorted",
			input:    []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace, configMap},
			expected: []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace, configMap},
		},
		{
			name:     "reverse order",
			input:    []*unstructured.Unstructured{configMap, namespace, apiExport, apiBinding, crd},
			expected: []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace, configMap},
		},
		{
			name:     "mixed order",
			input:    []*unstructured.Unstructured{namespace, configMap, crd, deployment, apiExport, apiBinding},
			expected: []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace, configMap, deployment},
		},
		{
			name:     "multiple objects of same kind",
			input:    []*unstructured.Unstructured{configMap, crd, configMap, crd},
			expected: []*unstructured.Unstructured{crd, crd, configMap, configMap},
		},
		{
			name:     "only regular objects",
			input:    []*unstructured.Unstructured{service, configMap, deployment},
			expected: []*unstructured.Unstructured{service, configMap, deployment},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.name, func(t *testing.T) {
			SortObjectsByDefaultHierarchy(tt.input)
			assert.ElementsMatch(t, tt.expected, tt.input)
		})
	}
}

func newUnstructured(apiVersion, kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": apiVersion,
			"kind":       kind,
			"metadata": map[string]any{
				"name": name,
			},
		},
	}
}
