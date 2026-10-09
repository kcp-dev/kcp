/*
Copyright 2021 The kcp Authors.

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

package crdpuller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	openapi_v2 "github.com/google/gnostic-models/openapiv2"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

func TestPuller(t *testing.T) {
	t.Parallel()
	getCRDCount := 0
	getCRDName := ""

	puller := &schemaPuller{
		serverGroupsAndResources: func() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
			return []*metav1.APIGroup{
					{
						Name: "",
						Versions: []metav1.GroupVersionForDiscovery{
							{
								GroupVersion: "v1",
								Version:      "v1",
							},
						},
						PreferredVersion: metav1.GroupVersionForDiscovery{
							GroupVersion: "v1",
							Version:      "v1",
						},
					},
					{
						Name: "metrics.k8s.io",
						Versions: []metav1.GroupVersionForDiscovery{
							{
								GroupVersion: "metrics.k8s.io/v1beta1",
								Version:      "v1beta1",
							},
						},
						PreferredVersion: metav1.GroupVersionForDiscovery{
							GroupVersion: "metrics.k8s.io/v1beta1",
							Version:      "v1beta1",
						},
					},
				}, []*metav1.APIResourceList{
					{
						GroupVersion: "v1",
						APIResources: []metav1.APIResource{
							{
								Name:       "pods",
								Namespaced: true,
								Kind:       "Pod",
							},
						},
					},
					{
						GroupVersion: "metrics.k8s.io/v1beta1",
						APIResources: []metav1.APIResource{
							{
								Name:       "pods",
								Namespaced: true,
								Kind:       "Pod",
							},
						},
					},
				}, nil
		},
		serverPreferredResources: func() ([]*metav1.APIResourceList, error) {
			return []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{
							Name:       "pods",
							Namespaced: false,
							Kind:       "Pod",
						},
					},
				},
			}, nil
		},
		getCRD: func(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			getCRDCount++
			getCRDName = name
			return &apiextensionsv1.CustomResourceDefinition{}, nil
		},
		resourceFor: func(groupResource schema.GroupResource) (schema.GroupResource, error) {
			return groupResource, nil
		},
	}

	_, err := puller.PullCRDs(context.Background(), runtime.NewScheme(), "pods")
	require.NoError(t, err, "error pulling")

	require.Equal(t, 1, getCRDCount)
	require.Equal(t, "pods.core", getCRDName)
}

func TestSchemaConverter_VisitReference_Extensions(t *testing.T) {
	t.Parallel()

	newSwagger := func() spec.Swagger {
		return spec.Swagger{
			SwaggerProps: spec.SwaggerProps{
				Swagger: "2.0",
				Info: &spec.Info{
					InfoProps: spec.InfoProps{
						Title:   "Test",
						Version: "v1",
					},
				},
				Definitions: spec.Definitions{},
			},
		}
	}
	newObjectSchema := func() spec.Schema {
		return spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:       []string{"object"},
				Properties: map[string]spec.Schema{},
			},
		}
	}
	newSchemaConverter := func() SchemaConverter {
		return SchemaConverter{
			schemaProps: &apiextensionsv1.JSONSchemaProps{},
			schemaName:  "test",
			description: "test",
			errors:      &[]error{},
			visited:     sets.New[string](),
		}
	}
	convertSwaggerToProtoModels := func(swagger *spec.Swagger) (proto.Models, error) {
		swaggerJson, err := swagger.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal swagger: %w", err)
		}

		doc, err := openapi_v2.ParseDocument(swaggerJson)
		if err != nil {
			return nil, fmt.Errorf("failed to parse swagger: %w", err)
		}

		models, err := proto.NewOpenAPIData(doc)
		if err != nil {
			return nil, fmt.Errorf("failed to build OpenAPI data: %w", err)
		}
		return models, nil
	}

	t.Run("x-kubernetes-preserve-unknown-fields", func(t *testing.T) {
		t.Parallel()

		newTestSwagger := func(refName string, extensions spec.Extensions) spec.Swagger {
			swagger := newSwagger()

			testSchema := newObjectSchema()
			testSchema.Properties["config"] = spec.Schema{
				SchemaProps: spec.SchemaProps{
					Ref: spec.MustCreateRef("#/definitions/" + refName),
				},
				VendorExtensible: spec.VendorExtensible{
					Extensions: extensions,
				},
			}

			swagger.SwaggerProps.Definitions["Test"] = testSchema
			swagger.SwaggerProps.Definitions[refName] = newObjectSchema()
			return swagger
		}

		getTestSwaggerConfigRef := func(swagger spec.Swagger) (proto.Reference, error) {
			models, err := convertSwaggerToProtoModels(&swagger)
			if err != nil {
				return nil, fmt.Errorf("failed to convert Swagger specification: %w", err)
			}

			testModel := models.LookupModel("Test")
			if testModel == nil {
				return nil, errors.New("test schema should be present")
			}

			testKind, ok := testModel.(*proto.Kind)
			if !ok {
				return nil, errors.New("test schema type should be proto.Kind")
			}

			configSchema, present := testKind.Fields["config"]
			if !present {
				return nil, errors.New("config field should be present")
			}

			configReference, ok := configSchema.(proto.Reference)
			if !ok {
				return nil, errors.New("config schema type should be proto.Reference")
			}

			return configReference, nil
		}

		t.Run("should error if value is not a valid boolean string", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": "test",
			}
			swagger := newTestSwagger("RawData", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)
			require.NotEmpty(t, schemaConverter.errors)

			err = (*schemaConverter.errors)[0]
			require.ErrorContains(t, err, "failed to parse 'x-kubernetes-preserve-unknown-fields' value")
		})

		t.Run("should error if value type is not supported", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": 1,
			}
			swagger := newTestSwagger("RawData", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)
			require.NotEmpty(t, schemaConverter.errors)

			err = (*schemaConverter.errors)[0]
			require.ErrorContains(t, err, "unsupported 'x-kubernetes-preserve-unknown-fields' value type")
		})

		t.Run("should not set JSONSchemaProps.XPreserveUnknownFields if extension is not present", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{}
			swagger := newTestSwagger("RawData", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)

			require.Empty(t, schemaConverter.errors)
			require.Nil(t, schemaConverter.schemaProps.XPreserveUnknownFields)
		})

		t.Run("should set JSONSchemaProps.XPreserveUnknownFields if extension is a valid boolean", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": true,
			}
			swagger := newTestSwagger("RawData", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)

			require.Empty(t, schemaConverter.errors)
			require.True(t, *schemaConverter.schemaProps.XPreserveUnknownFields)
		})

		t.Run("should set JSONSchemaProps.XPreserveUnknownFields if extension is a valid boolean string", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": "true",
			}
			swagger := newTestSwagger("RawData", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)

			require.Empty(t, schemaConverter.errors)
			require.True(t, *schemaConverter.schemaProps.XPreserveUnknownFields)
		})

		t.Run("should set JSONSchemaProps.XPreserveUnknownFields if reference is a known schema", func(t *testing.T) {
			t.Parallel()

			extensions := spec.Extensions{
				"x-kubernetes-preserve-unknown-fields": "true",
			}
			swagger := newTestSwagger("io.k8s.apimachinery.pkg.runtime.RawExtension", extensions)
			configReference, err := getTestSwaggerConfigRef(swagger)
			require.Nil(t, err)

			schemaConverter := newSchemaConverter()
			schemaConverter.VisitReference(configReference)

			require.Empty(t, schemaConverter.errors)
			require.Equal(t, "object", schemaConverter.schemaProps.Type)
			require.True(t, *schemaConverter.schemaProps.XPreserveUnknownFields)
		})
	})
}
