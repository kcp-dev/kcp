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

// Package crgen generates custom resources and API sharing objects
// (APIResourceSchema, APIExport, APIBinding) for use in kcp load testing.
// The generated resources have configurable size and complexity to approximate
// real-world workloads.
package crgen

import (
	"encoding/json"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// Dummy describes the desired shape and size of generated custom resources.
// It serves as the central configuration for all resource generation in this
// package — OpenAPI schemas, CR instances, and the typed API sharing objects
// (APIResourceSchema, APIExport, APIBinding) all derive from it.
// In order to allow creating multiple APIs from the same shape, generation
// functions take an additional id parameter. For loadtests this is simply
// the sequence number, but outside it can be used with any parameter.
type Dummy struct {
	// leafFields is the number of scalar string fields in spec (>= 1).
	// The first field is always "data" and is used for the CRUD update path.
	leafFields int

	// listItems is the number of items in a spec.items[] array.
	// Each item has 3 string sub-fields (name, value, description).
	// 0 means no list field is generated.
	listItems int

	// targetSizeBytes pads spec.data to reach this approximate total CR
	// JSON size. 0 means no padding is applied.
	targetSizeBytes int
}

// NewDummy creates a Dummy with the given parameters.
func NewDummy(leafFields, listItems, targetSizeBytes int) Dummy {
	return Dummy{
		leafFields:      leafFields,
		listItems:       listItems,
		targetSizeBytes: targetSizeBytes,
	}
}

// GVR returns the GroupVersionResource for the load test API identified by id.
func GVR(id string) schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "loadtest.kcp.io",
		Version:  "v1alpha1",
		Resource: pluralResource(id),
	}
}

// GenerateAPIResourceSchema returns a fully-formed APIResourceSchema for the
// load test API identified by id. The embedded OpenAPI validation schema
// matches the CR shape described by the Dummy config.
func (d Dummy) GenerateAPIResourceSchema(id string) *apisv1alpha1.APIResourceSchema {
	return &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: schemaObjectName(id),
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "loadtest.kcp.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     kindName(id),
				ListKind: listKindName(id),
				Plural:   pluralResource(id),
				Singular: singularResource(id),
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: d.GenerateOpenAPISchema(),
					},
				},
			},
		},
	}
}

// GenerateAPIExport returns a fully-formed APIExport for the load test API
// identified by id. The export references the APIResourceSchema produced by
// GenerateAPIResourceSchema with the same id.
func (d Dummy) GenerateAPIExport(id string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportObjectName(id),
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   pluralResource(id),
					Group:  "loadtest.kcp.io",
					Schema: schemaObjectName(id),
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
}

// GenerateAPIBinding returns a fully-formed APIBinding that binds to the export
// in the given provider workspace. providerID identifies which export is being
// bound; providerPath is the logical cluster path of the provider workspace.
func (d Dummy) GenerateAPIBinding(providerID string, providerPath string) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: bindingObjectName(providerID),
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath,
					Name: exportObjectName(providerID),
				},
			},
		},
	}
}

// GenerateOpenAPISchema returns a JSON-encoded OpenAPI v3 schema suitable for
// APIResourceSchema.Spec.Versions[].Schema.Raw. The schema validates CRs of
// the shape described by the Dummy config.
func (d Dummy) GenerateOpenAPISchema() []byte {
	specProps := map[string]any{
		"data": map[string]any{"type": "string"},
	}

	for i := 2; i <= d.leafFields; i++ {
		specProps[fmt.Sprintf("field%d", i)] = map[string]any{"type": "string"}
	}

	if d.listItems > 0 {
		specProps["items"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"value":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				},
			},
		}
	}

	openAPISchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"apiVersion": map[string]any{"type": "string"},
			"kind":       map[string]any{"type": "string"},
			"metadata":   map[string]any{"type": "object"},
			"spec": map[string]any{
				"type":       "object",
				"properties": specProps,
			},
		},
	}

	data, err := json.Marshal(openAPISchema)
	if err != nil {
		// Should not happen, as we build from static literals only
		panic(fmt.Sprintf("failed to marshal OpenAPI schema: %v", err))
	}

	return data
}

// GenerateCR returns a fully-formed *unstructured.Unstructured custom resource
// conforming to the schema produced by GenerateOpenAPISchema. The resourceID
// determines the Kind (LoadTestResource{resourceID}) and must match the id used
// in GenerateAPIResourceSchema. The instanceName is used for the object's
// metadata.name (loadtest-cr-{instanceName}) and should be unique within the
// namespace. If d.targetSizeBytes > 0, spec.data is padded to reach the target
// total JSON size.
func (d Dummy) GenerateCR(resourceID, instanceName string) *unstructured.Unstructured {
	spec := map[string]any{
		"data": "initial-value",
	}

	for i := 2; i <= d.leafFields; i++ {
		spec[fmt.Sprintf("field%d", i)] = fmt.Sprintf("value-%d", i)
	}

	if d.listItems > 0 {
		items := make([]any, d.listItems)
		for i := range d.listItems {
			items[i] = map[string]any{
				"name":        fmt.Sprintf("item-%d", i),
				"value":       fmt.Sprintf("value-%d", i),
				"description": fmt.Sprintf("description for item %d", i),
			}
		}
		spec["items"] = items
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "loadtest.kcp.io/v1alpha1",
			"kind":       kindName(resourceID),
			"metadata": map[string]any{
				"name":      fmt.Sprintf("loadtest-cr-%s", instanceName),
				"namespace": "default",
			},
			"spec": spec,
		},
	}

	d.padToTargetSize(obj)

	return obj
}

// padToTargetSize adjusts spec.data so the total JSON serialization reaches
// approximately targetSizeBytes. No-op if targetSizeBytes is 0 or the object
// already exceeds it.
func (d Dummy) padToTargetSize(obj *unstructured.Unstructured) {
	if d.targetSizeBytes <= 0 {
		return
	}

	data, err := json.Marshal(obj.Object)
	if err != nil {
		return
	}

	currentSize := len(data)
	if currentSize >= d.targetSizeBytes {
		return
	}

	deficit := d.targetSizeBytes - currentSize

	currentData, _, _ := unstructured.NestedString(obj.Object, "spec", "data")
	paddedData := currentData + strings.Repeat("x", deficit)
	_ = unstructured.SetNestedField(obj.Object, paddedData, "spec", "data")
}

// These derive deterministic resource names from a string id. All load test
// resources follow the same naming convention so that providers and consumers
// can reference each other by id alone.

func pluralResource(id string) string {
	return fmt.Sprintf("loadtestresources%s", id)
}

func singularResource(id string) string {
	return fmt.Sprintf("loadtestresource%s", id)
}

func kindName(id string) string {
	return fmt.Sprintf("LoadTestResource%s", id)
}

func listKindName(id string) string {
	return fmt.Sprintf("LoadTestResource%sList", id)
}

func schemaObjectName(id string) string {
	return fmt.Sprintf("v1alpha1.%s.loadtest.kcp.io", pluralResource(id))
}

func exportObjectName(id string) string {
	return fmt.Sprintf("loadtest-export-%s", id)
}

func bindingObjectName(id string) string {
	return fmt.Sprintf("loadtest-binding-%s", id)
}
