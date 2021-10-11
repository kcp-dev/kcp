package schemacompat

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func boolPtr(b bool) *bool {
	return &b
}

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
		// LCD is the same as existing.
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
		desc: "new has more properties, existing contains additional properties boolean",
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
		desc: "new has more properties, existing contains additional properties boolean",
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
		// LCD is the same as existing.
		wantLCD: &apiextensionsv1.JSONSchemaProps{
			Type: "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
				Allows: true,
				Schema: &apiextensionsv1.JSONSchemaProps{
					Type: "boolean",
				},
			},
		},
	}} {
		t.Run(c.desc, func(t *testing.T) {
			gotLCD, err := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), c.existing, c.new, c.narrowExisting)
			if d := cmp.Diff(c.wantErr, err); d != "" {
				t.Errorf("Error Diff(-want,+got): %s", d)
			}
			if d := cmp.Diff(c.wantLCD, gotLCD); d != "" {
				t.Errorf("LCD Diff(-want,+got): %s", d)
			}
		})
	}
}
