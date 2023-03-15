/*
Copyright 2023 The KCP Authors.

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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestCompileConversion(t *testing.T) {
	t.Parallel()

	crdYAML, err := embeddedResources.ReadFile("widgets-crd.yaml")
	require.NoError(t, err, "error reading widgets yaml")

	var crd apiextensionsv1.CustomResourceDefinition
	err = yaml.Unmarshal(crdYAML, &crd)
	require.NoError(t, err, "error unmarshalling crd")

	structuralSchemas := schemasForCRD(t, crd)

	tests := map[string]struct {
		conversion    apisv1alpha1.APIVersionConversion
		expectedError string
	}{
		"unable to find version": {
			conversion: apisv1alpha1.APIVersionConversion{
				From: "vNotHere",
			},
			expectedError: field.Invalid(field.NewPath("foo").Child("from"), "vNotHere", "unable to find structural schema for version").Error(),
		},
	}

	for testName, tc := range tests {
		// Needed to avoid t.Parallel() races
		tc := tc

		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			_, err = compileConversion(field.NewPath("foo"), &tc.conversion, structuralSchemas)
			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCompileRule(t *testing.T) {
	t.Parallel()

	crdYAML, err := embeddedResources.ReadFile("widgets-crd.yaml")
	require.NoError(t, err, "error reading widgets yaml")

	var crd apiextensionsv1.CustomResourceDefinition
	err = yaml.Unmarshal(crdYAML, &crd)
	require.NoError(t, err, "error unmarshalling crd")

	structuralSchemas := schemasForCRD(t, crd)

	tests := map[string]struct {
		field          string
		transformation string
		want           error
	}{
		"invalid field path - root doesn't exist": {
			field: "noFieldHere",
			want:  field.Invalid(field.NewPath("foo").Child("field"), "noFieldHere", `field "noFieldHere" doesn't exist`),
		},
		"invalid field path - subpath doesn't exist": {
			field: "spec.some.unknown.field",
			want:  field.Invalid(field.NewPath("foo").Child("field"), "spec.some.unknown.field", `field "spec.some" doesn't exist`),
		},
		"invalid field path - parent not an object": {
			field: "apiVersion.foo",
			want:  field.Invalid(field.NewPath("foo").Child("field"), "apiVersion.foo", `expected field "apiVersion" to be an object`),
		},
		"invalid transformation": {
			field:          "spec.firstName",
			transformation: "self.foo was here",
			want:           field.Invalid(field.NewPath("foo").Child("transformation"), "self.foo was here", "error compiling CEL program: ERROR: <input>"),
		},
	}

	for testName, tc := range tests {
		tc := tc

		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			rule := apisv1alpha1.APIConversionRule{
				Field:          tc.field,
				Destination:    "spec.someField",
				Transformation: tc.transformation,
			}

			cr, err := compileRule(field.NewPath("foo"), rule, structuralSchemas["v1"])
			require.Nil(t, cr)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.want.Error())
		})
	}
}

func schemasForCRD(t *testing.T, crd apiextensionsv1.CustomResourceDefinition) map[string]*structuralschema.Structural {
	t.Helper()

	structuralSchemas := make(map[string]*structuralschema.Structural)
	for _, v := range crd.Spec.Versions {
		if v.Schema == nil {
			continue
		}

		internalJSONSchemaProps := &apiextensionsinternal.JSONSchemaProps{}
		err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema.OpenAPIV3Schema, internalJSONSchemaProps, nil)
		require.NoError(t, err, "failed converting version %s validation to internal version", v.Name)

		structuralSchema, err := structuralschema.NewStructural(internalJSONSchemaProps)
		require.NoError(t, err, "error getting structural schema for version %s", v.Name)

		err = structuraldefaulting.PruneDefaults(structuralSchema)
		require.NoError(t, err, "error pruning defaults for version %s", v.Name)
		structuralSchemas[v.Name] = structuralSchema
	}
	return structuralSchemas
}
