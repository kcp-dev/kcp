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

package crgen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGenerateOpenAPISchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		leafFields        int
		listItems         int
		expectedSpecProps []string
		expectItems       bool
	}{
		{
			name:              "minimal single field",
			leafFields:        1,
			listItems:         0,
			expectedSpecProps: []string{"data"},
			expectItems:       false,
		},
		{
			name:              "multiple fields",
			leafFields:        5,
			listItems:         0,
			expectedSpecProps: []string{"data", "field2", "field3", "field4", "field5"},
			expectItems:       false,
		},
		{
			name:              "multiple fields and list items",
			leafFields:        2,
			listItems:         3,
			expectedSpecProps: []string{"data", "field2", "items"},
			expectItems:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := NewDummy(tt.leafFields, tt.listItems, 0)
			raw := d.GenerateOpenAPISchema()

			var parsed map[string]any
			require.NoError(t, json.Unmarshal(raw, &parsed))

			props, ok := parsed["properties"].(map[string]any)
			require.True(t, ok)
			require.Contains(t, props, "apiVersion")
			require.Contains(t, props, "kind")
			require.Contains(t, props, "metadata")
			require.Contains(t, props, "spec")

			specMap, ok := props["spec"].(map[string]any)
			require.True(t, ok)
			specProps, ok := specMap["properties"].(map[string]any)
			require.True(t, ok)

			require.Len(t, specProps, len(tt.expectedSpecProps))
			for _, key := range tt.expectedSpecProps {
				require.Contains(t, specProps, key)
			}

			if tt.expectItems {
				itemsSchema, ok := specProps["items"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "array", itemsSchema["type"])
				itemsInner, ok := itemsSchema["items"].(map[string]any)
				require.True(t, ok)
				itemProps, ok := itemsInner["properties"].(map[string]any)
				require.True(t, ok)
				require.Contains(t, itemProps, "name")
				require.Contains(t, itemProps, "value")
				require.Contains(t, itemProps, "description")
			}
		})
	}
}

func TestGenerateCR(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		leafFields      int
		listItems       int
		targetSizeBytes int
		resourceID      string
		instanceName    string
		verify          func(t *testing.T, obj *unstructured.Unstructured)
	}{
		{
			name:         "minimal",
			leafFields:   1,
			resourceID:   "1",
			instanceName: "1",
			verify: func(t *testing.T, obj *unstructured.Unstructured) {
				t.Helper()
				require.Equal(t, "loadtest.kcp.io/v1alpha1", obj.GetAPIVersion())
				require.Equal(t, "LoadTestResource1", obj.GetKind())
				require.Equal(t, "loadtest-cr-1", obj.GetName())
				require.Equal(t, "default", obj.GetNamespace())

				data, found, err := unstructured.NestedString(obj.Object, "spec", "data")
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "initial-value", data)
			},
		},
		{
			name:         "multiple fields and list",
			leafFields:   3,
			listItems:    2,
			resourceID:   "1",
			instanceName: "1",
			verify: func(t *testing.T, obj *unstructured.Unstructured) {
				t.Helper()
				spec, ok := obj.Object["spec"].(map[string]any)
				require.True(t, ok)

				require.Equal(t, "initial-value", spec["data"])
				require.Equal(t, "value-2", spec["field2"])
				require.Equal(t, "value-3", spec["field3"])

				items, ok := spec["items"].([]any)
				require.True(t, ok)
				require.Len(t, items, 2)

				item0, ok := items[0].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "item-0", item0["name"])
				require.Equal(t, "value-0", item0["value"])
				require.Contains(t, item0["description"], "item 0")
			},
		},
		{
			name:            "pads to target size",
			leafFields:      1,
			targetSizeBytes: 4096,
			resourceID:      "1",
			instanceName:    "1",
			verify: func(t *testing.T, obj *unstructured.Unstructured) {
				t.Helper()
				data, err := json.Marshal(obj.Object)
				require.NoError(t, err)
				require.InDelta(t, 4096, len(data), 10)
			},
		},
		{
			name:            "no padding when zero",
			leafFields:      1,
			targetSizeBytes: 0,
			resourceID:      "1",
			instanceName:    "1",
			verify: func(t *testing.T, obj *unstructured.Unstructured) {
				t.Helper()
				data, err := json.Marshal(obj.Object)
				require.NoError(t, err)
				require.Less(t, len(data), 200)
			},
		},
		{
			name:            "no padding when already larger",
			leafFields:      50,
			targetSizeBytes: 10,
			resourceID:      "1",
			instanceName:    "1",
			verify: func(t *testing.T, obj *unstructured.Unstructured) {
				t.Helper()
				data, found, err := unstructured.NestedString(obj.Object, "spec", "data")
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, "initial-value", data)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := NewDummy(tt.leafFields, tt.listItems, tt.targetSizeBytes)
			obj := d.GenerateCR(tt.resourceID, tt.instanceName)
			tt.verify(t, obj)
		})
	}
}

func TestGenerateAPIResourceSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		leafFields int
	}{
		{name: "single leaf", leafFields: 1},
		{name: "multiple leafs", leafFields: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d := NewDummy(tt.leafFields, 0, 0)
			ars := d.GenerateAPIResourceSchema("1")

			require.Equal(t, schemaObjectName("1"), ars.Name)
			require.Equal(t, "loadtest.kcp.io", ars.Spec.Group)
			require.Equal(t, apiextensionsv1.NamespaceScoped, ars.Spec.Scope)
			require.Equal(t, kindName("1"), ars.Spec.Names.Kind)
			require.Equal(t, listKindName("1"), ars.Spec.Names.ListKind)
			require.Equal(t, pluralResource("1"), ars.Spec.Names.Plural)
			require.Equal(t, singularResource("1"), ars.Spec.Names.Singular)

			require.Len(t, ars.Spec.Versions, 1)
			v := ars.Spec.Versions[0]
			require.Equal(t, "v1alpha1", v.Name)
			require.True(t, v.Served)
			require.True(t, v.Storage)
			require.NotEmpty(t, v.Schema.Raw)

			var parsed map[string]any
			require.NoError(t, json.Unmarshal(v.Schema.Raw, &parsed))
			require.Equal(t, "object", parsed["type"])
		})
	}
}

func TestGenerateAPIExport(t *testing.T) {
	t.Parallel()

	d := NewDummy(1, 0, 0)
	export := d.GenerateAPIExport("1")

	require.Equal(t, exportObjectName("1"), export.Name)
	require.Len(t, export.Spec.Resources, 1)

	res := export.Spec.Resources[0]
	require.Equal(t, pluralResource("1"), res.Name)
	require.Equal(t, "loadtest.kcp.io", res.Group)
	require.Equal(t, schemaObjectName("1"), res.Schema)
	require.NotNil(t, res.Storage.CRD)
}

func TestGenerateAPIBinding(t *testing.T) {
	t.Parallel()

	d := NewDummy(1, 0, 0)
	binding := d.GenerateAPIBinding("1", "root:org:provider-1")

	require.Equal(t, bindingObjectName("1"), binding.Name)
	require.NotNil(t, binding.Spec.Reference.Export)
	require.Equal(t, "root:org:provider-1", binding.Spec.Reference.Export.Path)
	require.Equal(t, exportObjectName("1"), binding.Spec.Reference.Export.Name)
}

func TestGVR(t *testing.T) {
	t.Parallel()

	gvr := GVR("1")
	require.Equal(t, schema.GroupVersionResource{
		Group:    "loadtest.kcp.io",
		Version:  "v1alpha1",
		Resource: "loadtestresources1",
	}, gvr)
}
