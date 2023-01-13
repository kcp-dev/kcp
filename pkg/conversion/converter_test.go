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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestPreserveMap(t *testing.T) {
	originalYAML := `
apiVersion: test.kcp.io/v2
spec:
    sub:
        one: two
    list:
    - a
    - b
    ignore: this
`
	originalMap := make(map[string]interface{})
	err := yaml.Unmarshal([]byte(originalYAML), &originalMap)
	require.NoError(t, err)
	original := &unstructured.Unstructured{Object: originalMap}

	conversions := []apisv1alpha1.APIVersionConversion{
		{
			From:     "v1",
			Preserve: []string{"ignore"},
		},
		{
			From:     "v2",
			Preserve: []string{"spec.sub.one", "spec.list"},
		},
		{
			From:     "v3",
			Preserve: []string{"ignore"},
		},
	}

	m, err := generatePreserveMap(original, conversions)
	require.NoError(t, err)

	restored := make(map[string]interface{})
	err = restoreFromPreserveMap(m, restored)
	require.NoError(t, err)

	expectedYAML := `
spec:
    sub:
        one: two
    list:
    - a
    - b
`

	expected := make(map[string]interface{})
	err = yaml.Unmarshal([]byte(expectedYAML), &expected)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(expected, restored))
}

func Test_convert_noTransformations(t *testing.T) {
	crd := decode[*apiextensionsv1.CustomResourceDefinition](t, `
spec:
  group: test.kcp.io
  names:
    kind: Widget
    listKind: WidgetList
    plural: widgets
    singular: widget
  scope: Cluster
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          apiVersion:
            type: string
          spec:
            type: object
            properties:
              unmodifiedV1Struct:
                type: object
                properties:
                  a:
                    type: string
                  b:
                    type: string
              originalV1Name:
                type: string
              f1:
                type: string
              f2:
                type: string
  - name: v2
    schema:
      openAPIV3Schema:
        type: object
        properties:
          apiVersion:
            type: string
          spec:
            type: object
            properties:
              unmodifiedV1Struct:
                type: object
                properties:
                  a:
                    type: string
                  b:
                    type: string
              renamedInV2:
                type: string
              splitInV2:
                type: object
                properties:
                  f1:
                    type: string
                  f2:
                    type: string
`)

	// NOTE: this has both v1 and v2 fields because the converter does not do any pruning - that is handled by other
	// server machinery.
	expectedV1 := toUnstructured(t, `
apiVersion: test.kcp.io/v1
metadata:
  name: foo
  annotations:
    preserve.conversion.apis.kcp.io/v2: "{\"spec.newInV2String\":\"new1\",\"spec.newInV2Struct\":{\"c\":\"d\"}}"
spec:
  unmodifiedV1Struct:
    a: b
  originalV1Name: rename-1
  f1: v1
  f2: v2
  newInV2String: new1
  newInV2Struct:
    c: d
  renamedInV2: rename-1
  splitInV2:
    f1: v1
    f2: v2
`)

	v2 := toUnstructured(t, `
apiVersion: test.kcp.io/v2
metadata:
  name: foo
spec:
  unmodifiedV1Struct:
    a: b
  renamedInV2: rename-1
  splitInV2:
    f1: v1
    f2: v2
  newInV2String: new1
  newInV2Struct:
    c: d
`)

	conversion := &apisv1alpha1.APIConversion{
		Spec: apisv1alpha1.APIConversionSpec{
			Conversions: []apisv1alpha1.APIVersionConversion{
				{
					From: "v1",
					To:   "v2",
					Rules: []apisv1alpha1.APIConversionRule{
						{
							Field:       "spec.originalV1Name",
							Destination: "spec.renamedInV2",
						},
						{
							Field:       "spec.f1",
							Destination: "spec.splitInV2.f1",
						},
						{
							Field:       "spec.f2",
							Destination: "spec.splitInV2.f2",
						},
					},
				},
				{
					From: "v2",
					To:   "v1",
					Rules: []apisv1alpha1.APIConversionRule{
						{
							Field:       "spec.renamedInV2",
							Destination: "spec.originalV1Name",
						},
						{
							Field:       "spec.splitInV2.f1",
							Destination: "spec.f1",
						},
						{
							Field:       "spec.splitInV2.f2",
							Destination: "spec.f2",
						},
					},
					Preserve: []string{"spec.newInV2String", "spec.newInV2Struct"},
				},
			},
		},
	}

	converter, err := NewConverter(crd, conversion, wait.ForeverTestTimeout)
	require.NoError(t, err, "error creating converter")

	ctx := context.Background()
	converted, err := converter.convert(ctx, v2, schema.GroupVersion{Group: "test.kcp.io", Version: "v1"})
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(expectedV1, converted))

	v1WithPreserveAnnotation := toUnstructured(t, `
apiVersion: test.kcp.io/v1
metadata:
  name: foo
  annotations:
    preserve.conversion.apis.kcp.io/v2: "{\"spec.newInV2String\":\"new1\",\"spec.newInV2Struct\":{\"c\":\"d\"}}"
spec:
  unmodifiedV1Struct:
    a: b
  originalV1Name: rename-1
  f1: v1
  f2: v2
`)

	// NOTE: this has both v1 and v2 fields because the converter does not do any pruning - that is handled by other
	// server machinery.
	expectedV2 := toUnstructured(t, `
apiVersion: test.kcp.io/v2
metadata:
  name: foo
spec:
  unmodifiedV1Struct:
    a: b
  originalV1Name: rename-1
  f1: v1
  f2: v2
  newInV2String: new1
  newInV2Struct:
    c: d
  renamedInV2: rename-1
  splitInV2:
    f1: v1
    f2: v2
`)

	converted, err = converter.convert(ctx, v1WithPreserveAnnotation, schema.GroupVersion{Group: "test.kcp.io", Version: "v2"})
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(expectedV2, converted))
}

func toUnstructured(t *testing.T, data string) *unstructured.Unstructured {
	t.Helper()
	m := map[string]interface{}{}
	err := yaml.Unmarshal([]byte(data), &m)
	require.NoError(t, err, "error unmarshalling")
	return &unstructured.Unstructured{Object: m}
}

func decode[T runtime.Object](t *testing.T, data string) (ret T) {
	t.Helper()
	err := yaml.Unmarshal([]byte(data), &ret)
	require.NoError(t, err)
	return ret
}
