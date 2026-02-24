/*
Copyright 2026 The KCP Authors.

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

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestChunkObjectsByHierarchy(t *testing.T) {
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
			chunks := ChunkObjectsByHierarchy(tt.input)

			require.Len(t, chunks, len(tt.expected), "Expected %d chunks, got %d", len(tt.expected), len(chunks))
			for i, chunk := range chunks {
				require.Len(t, chunk, len(tt.expected[i]), "Chunk %d: expected %d objects, got %d", i, len(tt.expected[i]), len(chunk))
				for j, obj := range chunk {
					require.Equal(t, tt.expected[i][j], obj, "Chunk %d, index %d: expected %s, got %s", i, j, tt.expected[i][j].GetKind(), obj.GetKind())
				}
			}
		})
	}
}

func TestSortObjectsByHierarchy(t *testing.T) {
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
			input:    []*unstructured.Unstructured{configMap, namespace, apiBinding, apiExport, crd},
			expected: []*unstructured.Unstructured{crd, apiExport, apiBinding, namespace, configMap},
		},
		{
			name:     "mixed order",
			input:    []*unstructured.Unstructured{namespace, configMap, crd, deployment, apiBinding, apiExport},
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
			SortObjectsByHierarchy(tt.input)

			if len(tt.input) != len(tt.expected) {
				t.Fatalf("Expected %d objects, got %d", len(tt.expected), len(tt.input))
			}

			for i, obj := range tt.input {
				if obj != tt.expected[i] {
					t.Fatalf("At index %d: expected %s, got %s", i, tt.expected[i].GetKind(), obj.GetKind())
				}
			}
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
