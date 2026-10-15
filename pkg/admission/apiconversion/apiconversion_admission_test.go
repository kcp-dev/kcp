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

package apiconversion

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func TestValidateInitialization(t *testing.T) {
	t.Parallel()

	t.Run("missing getAPIResourceSchema", func(t *testing.T) {
		t.Parallel()
		plugin := &apiConversionAdmission{
			Handler:       admission.NewHandler(admission.Create, admission.Update),
			dynamicClient: kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme()),
		}
		require.EqualError(t, plugin.ValidateInitialization(), "getAPIResourceSchema is unset")
	})

	t.Run("missing dynamicClient", func(t *testing.T) {
		t.Parallel()
		plugin := &apiConversionAdmission{
			Handler: admission.NewHandler(admission.Create, admission.Update),
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), "x")
			},
		}
		require.EqualError(t, plugin.ValidateInitialization(), "dynamicClient is unset")
	})

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		plugin := &apiConversionAdmission{
			Handler: admission.NewHandler(admission.Create, admission.Update),
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), "x")
			},
			dynamicClient: kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme()),
		}
		require.NoError(t, plugin.ValidateInitialization())
	})
}

//nolint:paralleltest // SetFeatureGateDuringTest mutates a process-wide gate; subtests must run serially.
func TestValidate(t *testing.T) {
	schemaName := "rev0002.widgets.example.io"
	clusterName := logicalcluster.Name("test")

	validConversion := &apisv1alpha1.APIConversion{
		ObjectMeta: metav1.ObjectMeta{Name: schemaName},
		Spec: apisv1alpha1.APIConversionSpec{
			Conversions: []apisv1alpha1.APIVersionConversion{
				{
					From: "v1",
					To:   "v2",
					Rules: []apisv1alpha1.APIConversionRule{
						{Field: ".spec.firstName", Destination: ".spec.name.first"},
					},
				},
			},
		},
	}

	invalidConversion := &apisv1alpha1.APIConversion{
		ObjectMeta: metav1.ObjectMeta{Name: schemaName},
		Spec: apisv1alpha1.APIConversionSpec{
			Conversions: []apisv1alpha1.APIVersionConversion{
				{
					From: "v1",
					To:   "v2",
					Rules: []apisv1alpha1.APIConversionRule{
						{Field: ".spec.doesNotExist", Destination: ".spec.name.first"},
					},
				},
			},
		},
	}

	widgetsSchema := newWidgetsAPIResourceSchema(t, schemaName)

	tests := map[string]struct {
		featureEnabled bool
		attr           admission.Attributes
		clusterName    logicalcluster.Name
		getSchema      func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error)
		withCRD        bool
		wantErr        string
	}{
		"feature gate disabled": {
			featureEnabled: false,
			attr:           createAttr(validConversion),
			clusterName:    clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, fmt.Errorf("unexpected schema lookup when feature gate is disabled")
			},
		},
		"ignores non-apiconversion resources": {
			featureEnabled: true,
			attr: admission.NewAttributesRecord(
				&unstructured.Unstructured{},
				nil,
				apisv1alpha1.Kind("APIExport").WithVersion("v1alpha1"),
				"",
				"widgets",
				apisv1alpha1.Resource("apiexports").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
			clusterName: clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, fmt.Errorf("unexpected schema lookup for non-apiconversion resource")
			},
		},
		"skips system bound CRDs cluster": {
			featureEnabled: true,
			attr:           createAttr(validConversion),
			clusterName:    apibinding.SystemBoundCRDsClusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, fmt.Errorf("unexpected schema lookup in system bound CRDs cluster")
			},
		},
		"valid rules against APIResourceSchema": {
			featureEnabled: true,
			attr:           createAttr(validConversion),
			clusterName:    clusterName,
			getSchema: func(_ logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
				if name != schemaName {
					return nil, fmt.Errorf("unexpected schema name %q", name)
				}
				return widgetsSchema, nil
			},
		},
		"invalid field against APIResourceSchema": {
			featureEnabled: true,
			attr:           createAttr(invalidConversion),
			clusterName:    clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return widgetsSchema, nil
			},
			wantErr: "error compiling conversion rules",
		},
		"schema getter non-notfound error": {
			featureEnabled: true,
			attr:           createAttr(validConversion),
			clusterName:    clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, fmt.Errorf("boom")
			},
			wantErr: "error getting APIResourceSchema",
		},
		"falls back to local CRD": {
			featureEnabled: true,
			attr:           createAttr(validConversion),
			clusterName:    clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), schemaName)
			},
			withCRD: true,
		},
		"schema and CRD missing": {
			featureEnabled: true,
			attr:           createAttr(validConversion),
			clusterName:    clusterName,
			getSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), schemaName)
			},
			wantErr: "error getting APIResourceSchema or verifying CustomResourceDefinition",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.APIConversion, tc.featureEnabled)

			fakeDynamic := kcpfakedynamic.NewSimpleDynamicClient(runtime.NewScheme())
			if tc.withCRD {
				crd := newWidgetsCRD(schemaName)
				crdU, err := runtime.DefaultUnstructuredConverter.ToUnstructured(crd)
				require.NoError(t, err)
				u := &unstructured.Unstructured{Object: crdU}
				u.SetAPIVersion("apiextensions.k8s.io/v1")
				u.SetKind("CustomResourceDefinition")
				u.SetName(schemaName)
				u.SetAnnotations(map[string]string{logicalcluster.AnnotationKey: clusterName.String()})
				require.NoError(t, fakeDynamic.Tracker().Cluster(clusterName.Path()).Add(u))
			}

			plugin := &apiConversionAdmission{
				Handler:              admission.NewHandler(admission.Create, admission.Update),
				getAPIResourceSchema: tc.getSchema,
				dynamicClient:        fakeDynamic,
			}

			ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: tc.clusterName})
			err := plugin.Validate(ctx, tc.attr, nil)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func createAttr(c *apisv1alpha1.APIConversion) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(c),
		nil,
		apisv1alpha1.Kind("APIConversion").WithVersion("v1alpha1"),
		"",
		c.Name,
		apisv1alpha1.Resource("apiconversions").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func newWidgetsAPIResourceSchema(t *testing.T, name string) *apisv1alpha1.APIResourceSchema {
	t.Helper()

	v1 := apisv1alpha1.APIResourceVersion{Name: "v1", Served: true, Storage: true}
	require.NoError(t, v1.SetSchema(&apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"apiVersion": {Type: "string"},
			"kind":       {Type: "string"},
			"metadata":   {Type: "object"},
			"spec": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"firstName": {Type: "string"},
					"lastName":  {Type: "string"},
				},
			},
		},
	}))

	v2 := apisv1alpha1.APIResourceVersion{Name: "v2", Served: true, Storage: false}
	require.NoError(t, v2.SetSchema(&apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"apiVersion": {Type: "string"},
			"kind":       {Type: "string"},
			"metadata":   {Type: "object"},
			"spec": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"name": {
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"first": {Type: "string"},
							"last":  {Type: "string"},
						},
					},
				},
			},
		},
	}))

	return &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "example.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Widget",
				ListKind: "WidgetList",
				Plural:   "widgets",
				Singular: "widget",
			},
			Scope:    apiextensionsv1.ClusterScoped,
			Versions: []apisv1alpha1.APIResourceVersion{v1, v2},
		},
	}
}

func newWidgetsCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apiextensions.k8s.io/v1",
			Kind:       "CustomResourceDefinition",
		},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Widget",
				ListKind: "WidgetList",
				Plural:   "widgets",
				Singular: "widget",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"apiVersion": {Type: "string"},
								"kind":       {Type: "string"},
								"metadata":   {Type: "object"},
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"firstName": {Type: "string"},
										"lastName":  {Type: "string"},
									},
								},
							},
						},
					},
				},
				{
					Name:    "v2",
					Served:  true,
					Storage: false,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"apiVersion": {Type: "string"},
								"kind":       {Type: "string"},
								"metadata":   {Type: "object"},
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"name": {
											Type: "object",
											Properties: map[string]apiextensionsv1.JSONSchemaProps{
												"first": {Type: "string"},
												"last":  {Type: "string"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
