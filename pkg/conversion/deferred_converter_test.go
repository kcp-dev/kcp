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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestDeferredConverter_getConverter(t *testing.T) {
	t.Parallel()

	crdYAML, err := embeddedResources.ReadFile("widgets-crd.yaml")
	require.NoError(t, err, "error reading widgets yaml")

	var crd apiextensionsv1.CustomResourceDefinition
	err = yaml.Unmarshal(crdYAML, &crd)
	require.NoError(t, err, "error unmarshalling crd")

	apiConversionYAML := `
spec:
  conversions:
  - from: v1
    to: v2
    rules:
    - field: .spec.firstName
      destination: .spec.name.first
    - field: .spec.lastName
      destination: .spec.name.last
      transformation: self
    - field: .spec.lastName
      destination: .spec.name.lastUpper
      transformation: self.upperAscii()
  - from: v2
    to: v1
    rules:
    - field: .spec.name.first
      destination: .spec.firstName
    - field: .spec.name.last
      destination: .spec.lastName
    preserve:
    - .spec.someNewField
`
	var apiConversion apisv1alpha1.APIConversion
	err = yaml.Unmarshal([]byte(apiConversionYAML), &apiConversion)
	require.NoError(t, err, "error unmarshalling APIConversion")

	tests := map[string]struct {
		apiConversionNotFound bool
		strategy              apiextensionsv1.ConversionStrategyType
		expectedType          interface{}
		wantErrorMatching     string
		wantNewConverterCalls int
	}{
		"APIConversion not found, strategy=none": {
			apiConversionNotFound: true,
			strategy:              apiextensionsv1.NoneConverter,
			expectedType:          conversion.NewNOPConverter(),
			wantNewConverterCalls: 0,
		},
		"APIConversion not found, strategy=webhook": {
			apiConversionNotFound: true,
			strategy:              apiextensionsv1.WebhookConverter,
			wantErrorMatching:     "is not supported for CRD",
			wantNewConverterCalls: 0,
		},
		"APIConversion not found, strategy=unknown": {
			apiConversionNotFound: true,
			strategy:              "something",
			wantErrorMatching:     "unknown conversion strategy",
			wantNewConverterCalls: 0,
		},
		"APIConversion exists": {
			expectedType:          &fakeConverter{},
			wantNewConverterCalls: 1,
		},
	}

	for testName, tc := range tests {
		tc := tc

		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			crd := crd.DeepCopy()
			crd.Spec.Conversion.Strategy = tc.strategy

			newConverterCalls := 0

			c := &deferredConverter{
				crd: crd,
				getAPIConversion: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIConversion, error) {
					require.Equal(t, clusterName, logicalcluster.Name("root"))
					require.Equal(t, name, "v1.widgets.kcp.io")

					if tc.apiConversionNotFound {
						return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiconversions"), name)
					}

					return &apiConversion, nil
				},
				newConverter: func(crd *apiextensionsv1.CustomResourceDefinition, apiConversion *apisv1alpha1.APIConversion) (conversion.CRConverter, error) {
					newConverterCalls++
					return &fakeConverter{}, nil
				},
			}

			converter, err := c.getConverter()
			if tc.wantErrorMatching == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErrorMatching)
				return
			}

			require.IsType(t, tc.expectedType, converter)
			require.Equal(t, tc.wantNewConverterCalls, newConverterCalls)

			// Make sure we return the cached converter if we try to get it again
			converter, err = c.getConverter()
			require.NoError(t, err)
			require.IsType(t, tc.expectedType, converter)
			require.Equal(t, tc.wantNewConverterCalls, newConverterCalls)
		})
	}
}

type fakeConverter struct{}

func (f *fakeConverter) Convert(in *unstructured.UnstructuredList, _ schema.GroupVersion) (*unstructured.UnstructuredList, error) {
	return in, nil
}
