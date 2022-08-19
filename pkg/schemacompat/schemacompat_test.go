/*
Copyright 2021 The KCP Authors.

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

package schemacompat

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/multierr"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestCompatibility(t *testing.T) {
	for _, c := range []struct {
		desc                   string
		existing, new, wantLCD *apiextensionsv1.JSONSchemaProps
		narrowExisting         bool
		wantErr                error
	}{{
		desc: "new has more properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
				"new":      {Type: "integer"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
	}, {
		desc: "new has fewer properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
				"new":      {Type: "integer"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
		wantErr: field.Invalid(
			field.NewPath("schema", "openAPISchema").Child("properties"),
			[]string{"new"},
			"properties have been removed in an incompatible way"),
	}, {
		desc: "new has fewer properties, narrow existing",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
				"new":      {Type: "integer"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
		narrowExisting: true,
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
	}, {
		desc: "new allows any property of a schema compatible with existing properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"prop1": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
				"prop2": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
						"subProp2": {Type: "string"},
					},
				},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
						"subProp2": {Type: "string"},
					},
				},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"prop1": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
				"prop2": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
						"subProp2": {Type: "string"},
					},
				},
			},
		},
	}, {
		desc: "new allows any property of a schema not compatible with existing properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"prop1": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
				"prop2": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
						"subProp2": {Type: "string"},
					},
				},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: false,
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
			},
		},
		wantErr: field.Invalid(
			field.NewPath("schema", "openAPISchema").Child("properties").Key("prop2").Child("properties"),
			[]string{"subProp2"},
			"properties have been removed in an incompatible way"),
	}, {
		desc: "new allows any property of a schema",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "string"},
			},
		},
	}, {
		desc: "new has more properties, existing contains number",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "number"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "number"},
				"new":      {Type: "integer"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "number"},
			},
		},
	}, {
		desc: "new has more properties, existing contains integer",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
				"new":      {Type: "number"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
	}, {
		desc: "new has more properties, existing contains XIntOrString",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type:         "",
			XIntOrString: true,
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type:         "",
			XIntOrString: true,
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
				"new":      {Type: "number"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type:         "",
			XIntOrString: true,
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
	}, {
		desc: "new has more properties, existing contains XPreserveUnknownFields",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type:                   "",
			XPreserveUnknownFields: boolPtr(true),
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type:                   "",
			XPreserveUnknownFields: boolPtr(true),
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
				"new":      {Type: "number"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type:                   "",
			XPreserveUnknownFields: boolPtr(true),
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "integer"},
			},
		},
	}, {
		desc: "new has more properties, existing contains boolean",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "boolean"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "boolean"},
				"new":      {Type: "number"},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "boolean"},
			},
		},
	}, {
		desc: "new has more properties, existing contains array",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"existing": {Type: "integer"},
					},
				},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"existing": {Type: "integer"},
						"new":      {Type: "number"},
					},
				},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"existing": {Type: "integer"},
					},
				},
			},
		},
	}, {
		desc: "new has more properties, existing contains additional properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
						"subProp2": {Type: "string"},
					},
				},
			},
		},
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"subProp1": {Type: "string"},
					},
				},
			},
		},
	}, {
		desc: "new has additional properties boolean, existing contains additional properties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "boolean",
				},
			},
		},
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
			},
		},
	}, {
		desc: "new has additional properties, existing contains additional properties boolean",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "boolean",
				},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
			},
		},
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "boolean",
				},
			},
		},
	}, {
		desc: "existing has properties, new has neither properties or additionalProperties",
		existing: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"existing": {Type: "boolean"},
			},
		},
		new: &apiextensionsv1.JSONSchemaProps{
			Type: "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"existing": {Type: "integer"},
					},
				},
			},
		},
		wantErr: multierr.Append(
			field.Invalid(
				field.NewPath("schema", "openAPISchema").Child("type"),
				"array",
				`The type changed (was "object", now "array")`,
			),
			field.Invalid(
				field.NewPath("schema", "openAPISchema").Child("properties"),
				[]string{"existing"},
				"properties value has been completely cleared in an incompatible way",
			),
		),
	}} {
		t.Run(c.desc, func(t *testing.T) {
			gotLCD, err := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), c.existing, c.new, c.narrowExisting)
			if c.wantErr != nil {
				if err == nil {
					t.Fatalf("expected err %v but got nil", c.wantErr)
				}

				if d := cmp.Diff(c.wantErr.Error(), err.Error()); d != "" {
					t.Errorf("Error Diff(-want,+got): %s", d)
				}
			} else if err != nil {
				t.Fatalf("unexpected err %v", err)
			}

			if d := cmp.Diff(c.wantLCD, gotLCD); d != "" {
				t.Errorf("LCD Diff(-want,+got): %s", d)
			}
		})
	}
}

func boolPtr(b bool) *bool {
	return &b
}
