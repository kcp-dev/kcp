/*
Copyright 2022 The KCP Authors.

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

package apibinding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestPhaseReconciler(t *testing.T) {
	tests := map[string]struct {
		apiBinding          *apisv1alpha1.APIBinding
		apiExport           *apisv1alpha1.APIExport
		apiResourceSchemas  map[string]*apisv1alpha1.APIResourceSchema
		wantPhase           apisv1alpha1.APIBindingPhaseType
		wantReconcileStatus reconcileStatus
	}{
		"empty phase becomes binding": {
			apiBinding:          &apisv1alpha1.APIBinding{},
			wantPhase:           apisv1alpha1.APIBindingPhaseBinding,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"bound becomes binding when export changes": {
			apiBinding: &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: "org:ws",
					Name:        "my-binding",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							WorkspaceName: "new-workspace",
							ExportName:    "new-export",
						},
					},
				},
				Status: apisv1alpha1.APIBindingStatus{
					BoundAPIExport: &apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							WorkspaceName: "some-workspace",
							ExportName:    "some-export",
						},
					},
					Phase: apisv1alpha1.APIBindingPhaseBound,
				},
			},
			wantPhase:           apisv1alpha1.APIBindingPhaseBinding,
			wantReconcileStatus: reconcileStatusStop,
		},
		"bound becomes binding when export changes what it's exporting": {
			apiBinding: &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: "org:ws",
					Name:        "my-binding",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							WorkspaceName: "some-workspace",
							ExportName:    "some-export",
						},
					},
				},
				Status: apisv1alpha1.APIBindingStatus{
					BoundAPIExport: &apisv1alpha1.ExportReference{
						Workspace: &apisv1alpha1.WorkspaceExportReference{
							WorkspaceName: "some-workspace",
							ExportName:    "some-export",
						},
					},
					BoundResources: []apisv1alpha1.BoundAPIResource{
						{
							Group:    "mygroup",
							Resource: "someresources",
							Schema: apisv1alpha1.BoundAPIResourceSchema{
								Name: "today.someresources.mygroup",
								UID:  "uid1",
							},
						},
						{
							Group:    "anothergroup",
							Resource: "otherresources",
							Schema: apisv1alpha1.BoundAPIResourceSchema{
								Name: "today.otherresources.anothergroup",
								UID:  "uid2",
							},
						},
					},
					Phase: apisv1alpha1.APIBindingPhaseBound,
				},
			},
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"someresources", "moreresources"},
				},
			},
			apiResourceSchemas: map[string]*apisv1alpha1.APIResourceSchema{
				"someresources": {
					ObjectMeta: metav1.ObjectMeta{
						Name: "someresources",
						UID:  "uid1",
					},
				},
				"moreresources": {
					ObjectMeta: metav1.ObjectMeta{
						Name: "moreresources",
						UID:  "uid3",
					},
				},
			},
			wantPhase:           apisv1alpha1.APIBindingPhaseBinding,
			wantReconcileStatus: reconcileStatusStop,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &phaseReconciler{
				getAPIExport: func(clusterName, name string) (*apisv1alpha1.APIExport, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					require.Equal(t, "some-export", name)
					return tc.apiExport, nil
				},
				getAPIResourceSchema: func(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					return tc.apiResourceSchemas[name], nil
				},
			}

			status, err := r.reconcile(context.Background(), tc.apiBinding)
			require.NoError(t, err)
			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantPhase, tc.apiBinding.Status.Phase)
		})
	}
}

func TestCRDFromAPIResourceSchema(t *testing.T) {
	tests := map[string]struct {
		schema  *apisv1alpha1.APIResourceSchema
		want    *apiextensionsv1.CustomResourceDefinition
		wantErr bool
	}{
		"full schema": {
			schema: &apisv1alpha1.APIResourceSchema{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: "my-cluster",
					Name:        "my-name",
					UID:         types.UID("my-uuid"),
				},
				Spec: apisv1alpha1.APIResourceSchemaSpec{
					Group: "my-group",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural:     "widgets",
						Singular:   "widget",
						ShortNames: []string{"w"},
						Kind:       "Widget",
						ListKind:   "WidgetList",
						Categories: []string{"things"},
					},
					Scope: apiextensionsv1.NamespaceScoped,
					Versions: []apisv1alpha1.APIResourceVersion{
						{
							Name:               "v1",
							Served:             true,
							Storage:            false,
							Deprecated:         true,
							DeprecationWarning: pointer.StringPtr("deprecated!"),
							Schema: runtime.RawExtension{
								Raw: []byte(`
{
	"description": "foo",
	"type": "object"
}
								`),
							},
							Subresources: &apiextensionsv1.CustomResourceSubresources{
								Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
								Scale: &apiextensionsv1.CustomResourceSubresourceScale{
									SpecReplicasPath:   ".spec.replicas",
									StatusReplicasPath: ".status.replicas",
									LabelSelectorPath:  pointer.StringPtr(".status.selector"),
								},
							},
							AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
								{
									Name:        "My Column",
									Type:        "string",
									Format:      "string",
									Description: "This is my column",
									Priority:    1,
									JSONPath:    ".spec.myColumn",
								},
							},
						},
						{
							Name:       "v2",
							Served:     true,
							Storage:    true,
							Deprecated: false,
							Schema: runtime.RawExtension{
								Raw: []byte(`
{
	"description": "foo",
	"type": "object"
}
								`),
							},
							Subresources: &apiextensionsv1.CustomResourceSubresources{
								Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
								Scale: &apiextensionsv1.CustomResourceSubresourceScale{
									SpecReplicasPath:   ".spec.replicas",
									StatusReplicasPath: ".status.replicas",
									LabelSelectorPath:  pointer.StringPtr(".status.selector"),
								},
							},
							AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
								{
									Name:        "My Column",
									Type:        "string",
									Format:      "string",
									Description: "This is my column",
									Priority:    1,
									JSONPath:    ".spec.myColumn",
								},
							},
						},
					},
				},
			},
			want: &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: shadowWorkspaceName,
					Name:        "my-uuid",
					Annotations: map[string]string{
						annotationBoundCRDKey:      "",
						annotationSchemaClusterKey: "my-cluster",
						annotationSchemaNameKey:    "my-name",
					},
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "my-group",
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural:     "widgets",
						Singular:   "widget",
						ShortNames: []string{"w"},
						Kind:       "Widget",
						ListKind:   "WidgetList",
						Categories: []string{"things"},
					},
					Scope: apiextensionsv1.NamespaceScoped,
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{
							Name:               "v1",
							Served:             true,
							Storage:            false,
							Deprecated:         true,
							DeprecationWarning: pointer.StringPtr("deprecated!"),
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Description: "foo",
									Type:        "object",
								},
							},
							Subresources: &apiextensionsv1.CustomResourceSubresources{
								Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
								Scale: &apiextensionsv1.CustomResourceSubresourceScale{
									SpecReplicasPath:   ".spec.replicas",
									StatusReplicasPath: ".status.replicas",
									LabelSelectorPath:  pointer.StringPtr(".status.selector"),
								},
							},
							AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
								{
									Name:        "My Column",
									Type:        "string",
									Format:      "string",
									Description: "This is my column",
									Priority:    1,
									JSONPath:    ".spec.myColumn",
								},
							},
						},
						{
							Name:       "v2",
							Served:     true,
							Storage:    true,
							Deprecated: false,
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Description: "foo",
									Type:        "object",
								},
							},
							Subresources: &apiextensionsv1.CustomResourceSubresources{
								Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
								Scale: &apiextensionsv1.CustomResourceSubresourceScale{
									SpecReplicasPath:   ".spec.replicas",
									StatusReplicasPath: ".status.replicas",
									LabelSelectorPath:  pointer.StringPtr(".status.selector"),
								},
							},
							AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
								{
									Name:        "My Column",
									Type:        "string",
									Format:      "string",
									Description: "This is my column",
									Priority:    1,
									JSONPath:    ".spec.myColumn",
								},
							},
						},
					},
					Conversion:            nil,
					PreserveUnknownFields: false,
				},
			},
			wantErr: false,
		},
		"error when schema is invalid": {
			schema: &apisv1alpha1.APIResourceSchema{
				Spec: apisv1alpha1.APIResourceSchemaSpec{
					Versions: []apisv1alpha1.APIResourceVersion{
						{
							Schema: runtime.RawExtension{
								Raw: []byte("invalid json"),
							},
						},
					},
				},
			},
			wantErr: true,
		},
	}
	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			got, err := crdFromAPIResourceSchema(tc.schema)

			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr: %v, got %v", tc.wantErr, err)
			}
			if tc.wantErr {
				return
			}

			require.Equal(t, tc.want, got)
		})
	}
}
