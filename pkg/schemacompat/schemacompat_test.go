package schemacompat

import (
	"testing"

	"github.com/stretchr/testify/assert"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestSuccessNewHasMoreProperties(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
			"new": {
				Type: "integer",
			},
		},
	}

	lcd, errs := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), existing, new, false)
	assert.Nil(t, errs, "Adding a property to a schema should not be an incompatibility error")
	assert.Equal(t, existing.DeepCopy(), lcd, "LCD should be the existing schema")
}

func TestErrorNewHasLessProperties(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
			"new": {
				Type: "integer",
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
		},
	}

	basePath := field.NewPath("schema", "openAPISchema")
	lcd, errs := EnsureStructuralSchemaCompatibility(basePath, existing, new, false)
	assert.Equal(t,
		errors.NewAggregate([]error{
			field.Invalid(
				basePath.Child("properties"),
				[]string{ "new" },
				"properties have been removed in an incompatible way")}),
		errs, "Adding a property to a schema should not be an incompatibility error")
	assert.Nil(t, lcd, "LCD should be nil")
}

func TestSuccessNewHasLessPropertiesNarrowExisting(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
			"new": {
				Type: "integer",
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
		},
	}

	lcd, errs := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), existing, new, true)
	assert.Nil(t, errs, "Removing a property from a schema should not be an incompatibility error if narrowing the existing as the LCD has been requested")
	assert.Equal(t, new.DeepCopy(), lcd, "LCD should be the new schema with one property removed")
}

func TestSuccessNewAllowsAnyPropertiesOfAschemaCompatibleWithAllExistingProperties(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"prop1": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
				},
			},
			"prop2": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
					"subProp2": {
						Type: "string",
					},
				},
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
			Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
					"subProp2": {
						Type: "string",
					},
				},
			},
		},
	}

	lcd, errs := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), existing, new, false)
	assert.Nil(t, errs, "Allowing any properties of a schema that is compatible with the schemas of all the existing fixed properties should not be an incompatibility error")
	assert.Equal(t, existing.DeepCopy(), lcd, "LCD should be the existing schema")
}

func TestErrorNewAllowsAnyPropertiesOfAschemaNotCompatibleWithAllExistingProperties(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"prop1": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
				},
			},
			"prop2": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
					"subProp2": {
						Type: "string",
					},
				},
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
			Allows: false,
			Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"subProp1": {
						Type: "string",
					},
				},
			},
		},
	}

	basePath := field.NewPath("schema", "openAPISchema")
	lcd, errs := EnsureStructuralSchemaCompatibility(basePath, existing, new, false)

	assert.Equal(t,
		errors.NewAggregate([]error{
			field.Invalid(
				basePath.Child("properties").Key("prop2").Child("properties"),
				[]string{ "subProp2" },
				"properties have been removed in an incompatible way")}),
		errs, "Allowing any properties of a schema that is not compatible with the schemas of all the existing fixed properties should be an incompatibility error")
	assert.Nil(t, lcd, "LCD should be nil")
}

func TestSuccessNewAllowsAnyPropertiesOfAnySchema(t *testing.T) {
	existing := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"existing": {
				Type: "string",
			},
		},
	}
	new := &apiextensionsv1.JSONSchemaProps{
		Type: "object",
		AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{
			Allows: true,
		},
	}

	lcd, errs := EnsureStructuralSchemaCompatibility(field.NewPath("schema", "openAPISchema"), existing, new, false)
	assert.Nil(t, errs, "Allowing any properties of any schema should not be an incompatibility error")
	assert.Equal(t, existing.DeepCopy(), lcd, "LCD should be the existing schema")
}
