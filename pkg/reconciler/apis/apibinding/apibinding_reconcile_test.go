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
	"errors"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/pointer"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// requireConditionMatches looks for a condition matching c in g. Only fields that are set in c are compared (Type is
// required, though). If c.Message is set, the test performed is contains rather than an exact match.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	actual := conditions.Get(g, c.Type)

	require.NotNil(t, actual, "missing condition %q", c.Type)

	if c.Status != "" {
		require.Equal(t, c.Status, actual.Status, "missing condition %q status %q", c.Type, c.Status)
	}

	if c.Severity != "" {
		require.Equal(t, c.Severity, actual.Severity, "missing condition %q severity %q", c.Type, c.Severity)
	}

	if c.Reason != "" {
		require.Equal(t, c.Reason, actual.Reason, "missing condition %q reason %q", c.Type, c.Reason)
	}

	if c.Message != "" {
		require.Contains(t, actual.Message, c.Message, "missing condition %q containing %q in message", c.Type, c.Message)
	}
}

var (
	unbound = newBindingBuilder().
		WithClusterName("org:ws").
		WithName("my-binding").
		WithExportReference("org:some-workspace", "some-export")

	binding = unbound.DeepCopy().WithPhase(apisv1alpha1.APIBindingPhaseBinding)

	rebinding = binding.DeepCopy().
		WithBoundResources(
			new(boundAPIResourceBuilder).
				WithGroupResource("kcp.dev", "widgets").
				WithSchema("today.widgets.kcp.dev", "todaywidgetsuid").
				WithStorageVersions("v0", "v1").
				BoundAPIResource,
		)

	invalidSchema = binding.DeepCopy().WithExportReference("org:some-workspace", "invalid-schema")

	bound = unbound.DeepCopy().
		WithPhase(apisv1alpha1.APIBindingPhaseBound).
		WithBoundResources(
			new(boundAPIResourceBuilder).
				WithGroupResource("mygroup", "someresources").
				WithSchema("today.someresources.mygroup", "uid1").
				BoundAPIResource,
			new(boundAPIResourceBuilder).
				WithGroupResource("anothergroup", "otherresources").
				WithSchema("today.someresources.anothergroup", "uid2").
				BoundAPIResource,
		)

	conflicting = unbound.DeepCopy().
		WithName("conflicting").
		WithPhase(apisv1alpha1.APIBindingPhaseBound).
		WithExportReference("org:some-workspace", "conflict").
		WithBoundResources(
			new(boundAPIResourceBuilder).
				WithGroupResource("kcp.dev", "widgets").
				WithSchema("another.widgets.kcp.dev", "anotherwidgetsuid").
				BoundAPIResource,
		)

	todayWidgetsAPIResourceSchema = &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "some-workspace",
			},
			Name: "today.widgets.kcp.dev",
			UID:  "todaywidgetsuid",
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "kcp.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "widgets",
				Singular: "widget",
				Kind:     "Widget",
				ListKind: "WidgetList",
			},
			Scope: "Namespace",
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`{"description":"foo","type":"object"}`),
					},
				},
			},
		},
	}

	someOtherWidgetsAPIResourceSchema = &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: "another.widgets.kcp.dev",
			UID:  "anotherwidgetsuid",
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "kcp.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "widgets",
				Singular: "widget",
				Kind:     "Widget",
				ListKind: "WidgetList",
			},
			Scope: "Namespace",
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`{"description":"foo","type":"object"}`),
					},
				},
			},
		},
	}
)

func TestReconcileNew(t *testing.T) {
	apiBinding := unbound.Build()

	c := &controller{}

	err := c.reconcile(context.Background(), apiBinding)
	require.NoError(t, err)
	require.Equal(t, apisv1alpha1.APIBindingPhaseBinding, apiBinding.Status.Phase)
	requireConditionMatches(t, apiBinding, conditions.FalseCondition(conditionsv1alpha1.ReadyCondition, "", "", ""))
}

func TestReconcileBinding(t *testing.T) {
	tests := map[string]struct {
		apiBinding                              *apisv1alpha1.APIBinding
		getAPIExportError                       error
		getAPIResourceSchemaError               error
		existingAPIBindings                     []*apisv1alpha1.APIBinding
		crdExists                               bool
		getCRDError                             error
		wantCreateCRD                           bool
		createCRDError                          error
		wantUpdateCRD                           bool
		updateCRDError                          error
		deletedCRDs                             []string
		wantError                               bool
		wantInvalidReference                    bool
		wantAPIExportNotFound                   bool
		wantAPIExportInternalError              bool
		wantWaitingForEstablished               bool
		wantAPIExportValid                      bool
		wantReady                               bool
		wantBoundAPIExport                      bool
		wantInitialBindingComplete              bool
		wantInitialBindingCompleteInternalError bool
		wantInitialBindingCompleteSchemaInvalid bool
		wantPhaseBound                          bool
		wantBoundResources                      []apisv1alpha1.BoundAPIResource
		wantNamingConflict                      bool
		crdEstablished                          bool
		crdStorageVersions                      []string
	}{
		"Update to nil workspace ref reports invalid APIExport": {
			apiBinding:           binding.DeepCopy().WithoutWorkspaceReference().Build(),
			wantInvalidReference: true,
		},
		"APIExport not found": {
			apiBinding:            binding.Build(),
			getAPIExportError:     apierrors.NewNotFound(apisv1alpha1.SchemeGroupVersion.WithResource("apiexports").GroupResource(), "some-export"),
			wantAPIExportNotFound: true,
		},
		"APIExport get error - random error": {
			apiBinding:                 binding.Build(),
			getAPIExportError:          errors.New("foo"),
			wantAPIExportInternalError: true,
			wantError:                  true,
		},
		"APIResourceSchema get error - not found": {
			apiBinding:                 binding.Build(),
			getAPIResourceSchemaError:  apierrors.NewNotFound(schema.GroupResource{}, "foo"),
			wantAPIExportInternalError: true,
			wantError:                  false,
		},
		"APIResourceSchema get error - random error": {
			apiBinding:                 binding.Build(),
			getAPIResourceSchemaError:  errors.New("foo"),
			wantAPIExportInternalError: true,
			wantError:                  true,
		},
		"APIExport doesn't have identity hash yet": {
			apiBinding: binding.DeepCopy().
				WithExportReference("org:some-workspace", "no-identity-hash").Build(),
			wantAPIExportValid: false,
		},
		"APIResourceSchema invalid": {
			apiBinding:                 invalidSchema.Build(),
			wantAPIExportInternalError: true,
		},
		"CRD get error": {
			apiBinding:                 binding.Build(),
			getCRDError:                errors.New("foo"),
			wantAPIExportInternalError: true,
			wantError:                  true,
		},
		"create CRD fails - invalid": {
			apiBinding:                              binding.Build(),
			wantCreateCRD:                           true,
			createCRDError:                          apierrors.NewInvalid(apiextensionsv1.Kind("CustomResourceDefinition"), "todaywidgetsuid", field.ErrorList{field.Forbidden(field.NewPath("foo"), "details")}),
			wantInitialBindingCompleteSchemaInvalid: true,
			wantError:                               false,
		},
		"create CRD fails - other error": {
			apiBinding:                              binding.Build(),
			wantCreateCRD:                           true,
			createCRDError:                          errors.New("foo"),
			wantInitialBindingCompleteInternalError: true,
			wantError:                               true,
		},
		"create CRD - no other bindings": {
			apiBinding:                binding.Build(),
			wantCreateCRD:             true,
			wantWaitingForEstablished: true,
			wantAPIExportValid:        true,
			wantBoundAPIExport:        true,
			wantBoundResources:        nil, // not yet established
		},
		"create CRD - other bindings - no conflicts": {
			apiBinding: binding.Build(),
			existingAPIBindings: []*apisv1alpha1.APIBinding{
				bound.Build(),
			},
			wantCreateCRD:             true,
			wantWaitingForEstablished: true,
			wantAPIExportValid:        true,
			wantBoundAPIExport:        true,
			wantBoundResources:        nil, // not yet established
		},
		"create CRD - other bindings - conflicts": {
			apiBinding: binding.Build(),
			existingAPIBindings: []*apisv1alpha1.APIBinding{
				conflicting.Build(),
			},
			wantNamingConflict: true,
		},
		"bind existing CRD - other bindings - conflicts": {
			apiBinding: binding.Build(),
			crdExists:  true,
			existingAPIBindings: []*apisv1alpha1.APIBinding{
				conflicting.Build(),
			},
			wantNamingConflict: true,
		},
		"CRD already exists but isn't established yet": {
			apiBinding:                binding.Build(),
			getCRDError:               nil,
			crdExists:                 true,
			crdEstablished:            false,
			crdStorageVersions:        []string{"v0", "v1"},
			wantAPIExportValid:        true,
			wantReady:                 false,
			wantBoundAPIExport:        true,
			wantBoundResources:        nil, // not established yet
			wantWaitingForEstablished: true,
		},
		"CRD already exists and is established": {
			apiBinding:         binding.Build(),
			getCRDError:        nil,
			crdExists:          true,
			crdEstablished:     true,
			crdStorageVersions: []string{"v0", "v1"},
			wantAPIExportValid: true,
			wantReady:          true,
			wantBoundAPIExport: true,
			wantBoundResources: []apisv1alpha1.BoundAPIResource{
				{
					Group:    "kcp.dev",
					Resource: "widgets",
					Schema: apisv1alpha1.BoundAPIResourceSchema{
						Name:         "today.widgets.kcp.dev",
						UID:          "todaywidgetsuid",
						IdentityHash: "hash1",
					},
					StorageVersions: []string{"v0", "v1"},
				},
			},
			wantPhaseBound:             true,
			wantInitialBindingComplete: true,
		},
		"Ensure merging storage versions works": {
			apiBinding:         rebinding.Build(),
			getCRDError:        nil,
			crdExists:          true,
			crdEstablished:     true,
			crdStorageVersions: []string{"v2"},
			wantAPIExportValid: true,
			wantReady:          true,
			wantBoundAPIExport: true,
			wantBoundResources: []apisv1alpha1.BoundAPIResource{
				{
					Group:    "kcp.dev",
					Resource: "widgets",
					Schema: apisv1alpha1.BoundAPIResourceSchema{
						Name:         "today.widgets.kcp.dev",
						UID:          "todaywidgetsuid",
						IdentityHash: "hash1",
					},
					StorageVersions: []string{"v0", "v1", "v2"},
				},
			},
			wantPhaseBound:             true,
			wantInitialBindingComplete: true,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			createCRDCalled := false

			apiExports := map[string]*apisv1alpha1.APIExport{
				"some-export": {
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "some-workspace",
						},
						Name: "some-export",
					},
					Spec: apisv1alpha1.APIExportSpec{
						LatestResourceSchemas: []string{"today.widgets.kcp.dev"},
					},
					Status: apisv1alpha1.APIExportStatus{IdentityHash: "hash1"},
				},
				"conflict": {
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "some-workspace",
						},
						Name: "conflict",
					},
					Spec: apisv1alpha1.APIExportSpec{
						LatestResourceSchemas: []string{"another.widgets.kcp.dev"},
					},
					Status: apisv1alpha1.APIExportStatus{IdentityHash: "hash2"},
				},
				"invalid-schema": {
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "some-workspace",
						},
						Name: "invalid-schema",
					},
					Spec: apisv1alpha1.APIExportSpec{
						LatestResourceSchemas: []string{"invalid.schema.io"},
					},
					Status: apisv1alpha1.APIExportStatus{IdentityHash: "hash3"},
				},
				"no-identity-hash": {
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "some-workspace",
						},
						Name: "some-export",
					},
					Spec: apisv1alpha1.APIExportSpec{
						LatestResourceSchemas: []string{"today.widgets.kcp.dev"},
					},
				},
			}

			apiResourceSchemas := map[string]*apisv1alpha1.APIResourceSchema{
				"invalid.schema.io": {
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							logicalcluster.AnnotationKey: "some-workspace",
						},
						Name: "invalid.schema.io",
					},
					Spec: apisv1alpha1.APIResourceSchemaSpec{
						Versions: []apisv1alpha1.APIResourceVersion{
							{
								Schema: runtime.RawExtension{
									Raw: []byte("invalid schema"),
								},
							},
						},
					},
				},
				"today.widgets.kcp.dev":   todayWidgetsAPIResourceSchema,
				"another.widgets.kcp.dev": someOtherWidgetsAPIResourceSchema,
			}

			c := &controller{
				listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
					return tc.existingAPIBindings, nil
				},
				getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
					require.Equal(t, "org:some-workspace", clusterName.String())
					return apiExports[name], tc.getAPIExportError
				},
				getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if tc.getAPIResourceSchemaError != nil {
						return nil, tc.getAPIResourceSchemaError
					}

					require.Equal(t, "org:some-workspace", clusterName.String())

					schema, ok := apiResourceSchemas[name]
					if !ok {
						return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
					}

					return schema, nil
				},
				getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					require.Equal(t, ShadowWorkspaceName, clusterName)

					if tc.getCRDError != nil {
						return nil, tc.getCRDError
					}

					crd := &apiextensionsv1.CustomResourceDefinition{
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							StoredVersions: tc.crdStorageVersions,
						},
					}

					if name == "anotherwidgetsuid" {
						crd.Spec.Group = "kcp.dev"
						crd.Spec.Names = apiextensionsv1.CustomResourceDefinitionNames{
							Plural: "widgets",
						}
						crd.Status.AcceptedNames = apiextensionsv1.CustomResourceDefinitionNames{
							Plural: "widgets",
						}
						return crd, nil
					}

					if !tc.crdExists {
						return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
					}

					if tc.crdEstablished {
						crd.Status.Conditions = append(crd.Status.Conditions, apiextensionsv1.CustomResourceDefinitionCondition{
							Type:   apiextensionsv1.Established,
							Status: apiextensionsv1.ConditionTrue,
						})
					}

					return crd, nil
				},
				listCRDs: func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return nil, nil
				},
				createCRD: func(ctx context.Context, clusterName logicalcluster.Path, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
					createCRDCalled = true
					return crd, tc.createCRDError
				},
				deletedCRDTracker: &lockedStringSet{},
			}

			err := c.reconcile(context.Background(), tc.apiBinding)

			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.wantCreateCRD, createCRDCalled, "mismatch on CRD creation expectation")

			if tc.wantInvalidReference {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.APIExportInvalidReferenceReason,
				})
			}

			if tc.wantAPIExportNotFound {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.APIExportNotFoundReason,
				})
			}

			if tc.wantAPIExportInternalError {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.InternalErrorReason,
				})
			}

			if tc.wantWaitingForEstablished {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.InitialBindingCompleted,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityInfo,
					Reason:   apisv1alpha1.WaitingForEstablishedReason,
				})
			}

			if tc.wantAPIExportValid {
				requireConditionMatches(t, tc.apiBinding, conditions.TrueCondition(apisv1alpha1.APIExportValid))
			}

			if tc.wantReady {
				requireConditionMatches(t, tc.apiBinding, conditions.TrueCondition(conditionsv1alpha1.ReadyCondition))
			} else {
				requireConditionMatches(t, tc.apiBinding, conditions.FalseCondition(conditionsv1alpha1.ReadyCondition, "", "", ""))
			}

			if tc.wantInitialBindingComplete {
				requireConditionMatches(t, tc.apiBinding, conditions.TrueCondition(apisv1alpha1.InitialBindingCompleted))
			}

			if tc.wantPhaseBound {
				require.Equal(t, apisv1alpha1.APIBindingPhaseBound, tc.apiBinding.Status.Phase)
			} else {
				require.Equal(t, apisv1alpha1.APIBindingPhaseBinding, tc.apiBinding.Status.Phase)
			}

			require.Len(t, tc.apiBinding.Status.BoundResources, len(tc.wantBoundResources), "unexpected bound resources")

			for _, want := range tc.wantBoundResources {
				found := false

				for _, got := range tc.apiBinding.Status.BoundResources {
					if got.Group != want.Group || got.Resource != want.Resource {
						continue
					}

					found = true

					require.Equal(t, want, got)
				}

				require.True(t, found, "expected bound resource group=%s resource=%s", want.Group, want.Resource)
			}

			if tc.wantNamingConflict {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.InitialBindingCompleted,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.NamingConflictsReason,
					Message:  "naming conflict with a bound API conflicting, spec.names.plural=widgets is forbidden",
				})
			}

			if tc.wantInitialBindingCompleteInternalError {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.InitialBindingCompleted,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.InternalErrorReason,
				})
			}

			if tc.wantInitialBindingCompleteSchemaInvalid {
				requireConditionMatches(t, tc.apiBinding, &conditionsv1alpha1.Condition{
					Type:     apisv1alpha1.InitialBindingCompleted,
					Status:   corev1.ConditionFalse,
					Severity: conditionsv1alpha1.ConditionSeverityError,
					Reason:   apisv1alpha1.APIResourceSchemaInvalidReason,
				})
			}
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
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "my-cluster",
					},
					Name: "my-name",
					UID:  types.UID("my-uuid"),
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
							Subresources: apiextensionsv1.CustomResourceSubresources{
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
							Subresources: apiextensionsv1.CustomResourceSubresources{
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
					Name: "my-uuid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:            ShadowWorkspaceName.String(),
						apisv1alpha1.AnnotationBoundCRDKey:      "",
						apisv1alpha1.AnnotationSchemaClusterKey: "my-cluster",
						apisv1alpha1.AnnotationSchemaNameKey:    "my-name",
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
			got, err := generateCRD(tc.schema)

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

// TODO(ncdc): this is a modified copy from apibinding admission. Unify these into a reusable package.
type bindingBuilder struct {
	apisv1alpha1.APIBinding
}

func newBindingBuilder() *bindingBuilder {
	b := new(bindingBuilder)
	conditions.MarkFalse(b, apisv1alpha1.InitialBindingCompleted, "", "", "")
	return b
}

func (b *bindingBuilder) DeepCopy() *bindingBuilder {
	return &bindingBuilder{
		APIBinding: *b.APIBinding.DeepCopy(),
	}
}

func (b *bindingBuilder) Build() *apisv1alpha1.APIBinding {
	return b.APIBinding.DeepCopy()
}

func (b *bindingBuilder) WithClusterName(clusterName string) *bindingBuilder {
	if b.Annotations == nil {
		b.Annotations = make(map[string]string)
	}
	b.Annotations[logicalcluster.AnnotationKey] = clusterName
	return b
}

func (b *bindingBuilder) WithName(name string) *bindingBuilder {
	b.Name = name
	return b
}

func (b *bindingBuilder) WithoutWorkspaceReference() *bindingBuilder {
	b.Spec.Reference.Export = nil
	return b
}

func (b *bindingBuilder) WithExportReference(cluster logicalcluster.Name, exportName string) *bindingBuilder {
	b.Spec.Reference.Export = &apisv1alpha1.ExportBindingReference{
		Cluster: cluster,
		Name:    exportName,
	}
	return b
}

func (b *bindingBuilder) WithPhase(phase apisv1alpha1.APIBindingPhaseType) *bindingBuilder {
	b.Status.Phase = phase
	return b
}

func (b *bindingBuilder) WithBoundResources(boundResources ...apisv1alpha1.BoundAPIResource) *bindingBuilder {
	b.Status.BoundResources = boundResources
	return b
}

type boundAPIResourceBuilder struct {
	apisv1alpha1.BoundAPIResource
}

func (b *boundAPIResourceBuilder) WithGroupResource(group, resource string) *boundAPIResourceBuilder {
	b.Group = group
	b.Resource = resource
	return b
}

func (b *boundAPIResourceBuilder) WithSchema(name, uid string) *boundAPIResourceBuilder {
	b.Schema = apisv1alpha1.BoundAPIResourceSchema{
		Name: name,
		UID:  uid,
	}
	return b
}

func (b *boundAPIResourceBuilder) WithStorageVersions(v ...string) *boundAPIResourceBuilder {
	b.StorageVersions = v
	return b
}
