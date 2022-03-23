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

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type wantCondition struct {
	Type     conditionsv1alpha1.ConditionType
	Status   corev1.ConditionStatus
	Reason   string
	Severity conditionsv1alpha1.ConditionSeverity
}

func requireConditionMatches(t *testing.T, g conditions.Getter, w wantCondition) {
	c := conditions.Get(g, w.Type)
	require.NotNil(t, c, "missing condition %q", w.Type)
	require.Equal(t, w.Status, c.Status)
	require.Equal(t, w.Severity, c.Severity)
	require.Equal(t, w.Reason, c.Reason)
}

var (
	unbound = new(bindingBuilder).
		WithClusterName("org:ws").
		WithName("my-binding").
		WithWorkspaceReference("some-workspace", "some-export")

	bound = unbound.DeepCopy().
		WithPhase(apisv1alpha1.APIBindingPhaseBound).
		WithBoundAPIExport("some-workspace", "some-export").
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
)

func TestPhaseReconciler(t *testing.T) {
	tests := map[string]struct {
		apiBinding          *apisv1alpha1.APIBinding
		apiExport           *apisv1alpha1.APIExport
		getAPIExportError   error
		apiResourceSchemas  map[string]*apisv1alpha1.APIResourceSchema
		wantReconcileStatus reconcileStatus
		wantBinding         bool
		wantBound           bool
		wantCondition       *wantCondition
		wantError           bool
	}{
		"empty phase becomes binding": {
			apiBinding:          unbound.Build(),
			wantBinding:         true,
			wantReconcileStatus: reconcileStatusContinue,
		},
		"bound becomes binding when referenced export changes": {
			apiBinding: bound.DeepCopy().
				WithWorkspaceReference("new-workspace", "new-export").
				Build(),
			wantBinding:         true,
			wantReconcileStatus: reconcileStatusStop,
		},
		"bound becomes binding when export changes what it's exporting": {
			apiBinding: bound.Build(),
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
			wantBinding:         true,
			wantReconcileStatus: reconcileStatusStop,
		},
		"bound becomes binding when bound APIResourceSchema UID changes": {
			apiBinding: bound.Build(),
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"someresources", "otherresources"},
				},
			},
			apiResourceSchemas: map[string]*apisv1alpha1.APIResourceSchema{
				"someresources": {
					ObjectMeta: metav1.ObjectMeta{
						Name: "someresources",
						UID:  "uid1",
					},
				},
				"otherresources": {
					ObjectMeta: metav1.ObjectMeta{
						Name: "otherresources",
						UID:  "newuid",
					},
				},
			},
			wantBinding:         true,
			wantReconcileStatus: reconcileStatusStop,
		},
		"APIExportValid warning condition set when error getting previously bound APIExport": {
			apiBinding:          bound.Build(),
			getAPIExportError:   errors.New("foo"),
			wantError:           true,
			wantReconcileStatus: reconcileStatusStop,
			wantCondition: &wantCondition{
				Type:     apisv1alpha1.APIExportValid,
				Status:   corev1.ConditionFalse,
				Reason:   apisv1alpha1.GetErrorReason,
				Severity: conditionsv1alpha1.ConditionSeverityWarning,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := &phaseReconciler{
				getAPIExport: func(clusterName, name string) (*apisv1alpha1.APIExport, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					require.Equal(t, "some-export", name)
					return tc.apiExport, tc.getAPIExportError
				},
				getAPIResourceSchema: func(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					return tc.apiResourceSchemas[name], nil
				},
			}

			status, err := r.reconcile(context.Background(), tc.apiBinding)

			gotErr := err != nil
			require.Equal(t, tc.wantError, gotErr)
			if tc.wantError {
				return
			}

			require.Equal(t, status, tc.wantReconcileStatus)

			switch {
			case tc.wantBinding:
				require.Equal(t, apisv1alpha1.APIBindingPhaseBinding, tc.apiBinding.Status.Phase)
			case tc.wantBound:
				require.Equal(t, apisv1alpha1.APIBindingPhaseBound, tc.apiBinding.Status.Phase)
			}

			if tc.wantCondition != nil {
				requireConditionMatches(t, tc.apiBinding, *tc.wantCondition)
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

func TestWorkspaceAPIExportReferenceReconciler(t *testing.T) {
	tests := map[string]struct {
		apiBinding                *apisv1alpha1.APIBinding
		apiExport                 *apisv1alpha1.APIExport
		getAPIExportError         error
		apiResourceSchemas        map[string]*apisv1alpha1.APIResourceSchema
		getAPIResourceSchemaError error
		crds                      map[string]*apiextensionsv1.CustomResourceDefinition
		getCRDError               error
		wantCreateCRD             bool
		createCRDError            error
		wantUpdateCRD             bool
		updateCRDError            error
		deletedCRDs               []string
		wantReconcileStatus       reconcileStatus
		wantError                 bool
		wantConditions            []wantCondition
	}{
		"Bound updated with nil workspace ref unbinds": {
			apiBinding:          bound.DeepCopy().WithoutWorkspaceReference().Build(),
			wantReconcileStatus: reconcileStatusStop,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.APIExportInvalidReferenceReason,
					Severity: conditionsv1alpha1.ConditionSeverityError,
				},
			},
		},
		"Bound, APIExport not found": {
			apiBinding:          bound.Build(),
			getAPIExportError:   apierrors.NewNotFound(apisv1alpha1.SchemeGroupVersion.WithResource("apiexports").GroupResource(), "some-export"),
			wantReconcileStatus: reconcileStatusStop,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.APIExportNotFoundReason,
					Severity: conditionsv1alpha1.ConditionSeverityError,
				},
			},
		},
		"APIExport get error": {
			apiBinding: new(bindingBuilder).
				WithClusterName("org:some-workspace").
				WithName("binding").
				WithWorkspaceReference("some-workspace", "some-export").
				WithBoundAPIExport("some-workspace", "some-export").
				WithBoundResources(
					new(boundAPIResourceBuilder).
						WithGroupResource("group", "resource").
						WithSchema("schema1", "uid1").
						BoundAPIResource,
				).
				Build(),
			getAPIExportError:   errors.New("foo"),
			wantReconcileStatus: reconcileStatusStop,
			wantError:           true,
			wantConditions: []wantCondition{
				{
					Type:   apisv1alpha1.APIExportValid,
					Status: corev1.ConditionUnknown,
					Reason: apisv1alpha1.GetErrorReason,
				},
			},
		},
		"APIResourceSchema get error": {
			apiBinding: new(bindingBuilder).
				WithClusterName("org:some-workspace").
				WithName("binding").
				WithWorkspaceReference("some-workspace", "some-export").
				WithBoundAPIExport("some-workspace", "some-export").
				WithBoundResources(
					new(boundAPIResourceBuilder).
						WithGroupResource("group", "resource").
						WithSchema("schema1", "uid1").
						BoundAPIResource,
				).
				Build(),
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"schema1"},
				},
			},
			getAPIResourceSchemaError: errors.New("foo"),
			wantReconcileStatus:       reconcileStatusStop,
			wantError:                 true,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.GetErrorReason,
					Severity: conditionsv1alpha1.ConditionSeverityError,
				},
			},
		},
		"APIResourceSchema invalid": {
			apiBinding: new(bindingBuilder).
				WithClusterName("org:some-workspace").
				WithName("binding").
				WithWorkspaceReference("some-workspace", "some-export").
				WithBoundAPIExport("some-workspace", "some-export").
				WithBoundResources(
					new(boundAPIResourceBuilder).
						WithGroupResource("group", "resource").
						WithSchema("schema1", "uid1").
						BoundAPIResource,
				).
				Build(),
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"schema1"},
				},
			},
			apiResourceSchemas: map[string]*apisv1alpha1.APIResourceSchema{
				"schema1": {
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
			},
			wantReconcileStatus: reconcileStatusStop,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.APIExportValid,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.InvalidSchemaReason,
					Severity: conditionsv1alpha1.ConditionSeverityError,
				},
			},
		},
		"CRD get error": {
			apiBinding: new(bindingBuilder).
				WithClusterName("org:some-workspace").
				WithName("binding").
				WithWorkspaceReference("some-workspace", "some-export").
				WithBoundAPIExport("some-workspace", "some-export").
				WithBoundResources(
					new(boundAPIResourceBuilder).
						WithGroupResource("group", "resource").
						WithSchema("schema1", "uid1").
						BoundAPIResource,
				).
				Build(),
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"schema1"},
				},
			},
			apiResourceSchemas: map[string]*apisv1alpha1.APIResourceSchema{
				"schema1": {},
			},
			getCRDError:         errors.New("foo"),
			wantReconcileStatus: reconcileStatusStop,
			wantError:           true,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.CRDReady,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.GetErrorReason,
					Severity: conditionsv1alpha1.ConditionSeverityError,
				},
			},
		},
		"create CRD": {
			apiBinding: new(bindingBuilder).
				WithClusterName("org:some-workspace").
				WithName("binding").
				WithWorkspaceReference("some-workspace", "some-export").
				WithBoundAPIExport("some-workspace", "some-export").
				WithBoundResources(
					new(boundAPIResourceBuilder).
						WithGroupResource("group", "resource").
						WithSchema("schema1", "uid1").
						BoundAPIResource,
				).
				Build(),
			apiExport: &apisv1alpha1.APIExport{
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{"schema1"},
				},
			},
			apiResourceSchemas: map[string]*apisv1alpha1.APIResourceSchema{
				"schema1": {},
			},
			getCRDError:         apierrors.NewNotFound(schema.GroupResource{}, ""),
			wantCreateCRD:       true,
			wantReconcileStatus: reconcileStatusStop,
			wantConditions: []wantCondition{
				{
					Type:     apisv1alpha1.CRDReady,
					Status:   corev1.ConditionFalse,
					Reason:   apisv1alpha1.WaitingForEstablishedReason,
					Severity: conditionsv1alpha1.ConditionSeverityInfo,
				},
				{
					Type:   apisv1alpha1.APIExportValid,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			createCRDCalled := false

			r := &workspaceAPIExportReferenceReconciler{
				getAPIExport: func(clusterName, name string) (*apisv1alpha1.APIExport, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					require.Equal(t, "some-export", name)
					return tc.apiExport, tc.getAPIExportError
				},
				getAPIResourceSchema: func(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error) {
					require.Equal(t, "org:some-workspace", clusterName)
					return tc.apiResourceSchemas[name], tc.getAPIResourceSchemaError
				},
				getCRD: func(clusterName, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					require.Equal(t, shadowWorkspaceName, clusterName)
					return tc.crds[name], tc.getCRDError
				},
				createCRD: func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
					createCRDCalled = true
					// We don't use the returned CRD - update in the future if we need it
					return &apiextensionsv1.CustomResourceDefinition{}, tc.createCRDError
				},
				updateCRD: func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
					return nil, nil
				},
				deletedCRDTracker: &lockedStringSet{},
			}

			status, err := r.reconcile(context.Background(), tc.apiBinding)

			gotErr := err != nil
			require.Equal(t, tc.wantError, gotErr)
			if tc.wantError {
				return
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantCreateCRD, createCRDCalled, "expected CRD to be created")

			for _, wantCondition := range tc.wantConditions {
				requireConditionMatches(t, tc.apiBinding, wantCondition)
			}
		})
	}
}

// TODO(ncdc): this is a modified copy from apibinding admission. Unify these into a reusable package.
type bindingBuilder struct {
	apisv1alpha1.APIBinding
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
	b.ClusterName = clusterName
	return b
}

func (b *bindingBuilder) WithName(name string) *bindingBuilder {
	b.Name = name
	return b
}

func (b *bindingBuilder) WithoutWorkspaceReference() *bindingBuilder {
	b.Spec.Reference.Workspace = nil
	return b
}

func (b *bindingBuilder) WithWorkspaceReference(workspaceName, exportName string) *bindingBuilder {
	b.Spec.Reference.Workspace = &apisv1alpha1.WorkspaceExportReference{
		WorkspaceName: workspaceName,
		ExportName:    exportName,
	}
	return b
}

func (b *bindingBuilder) WithPhase(phase apisv1alpha1.APIBindingPhaseType) *bindingBuilder {
	b.Status.Phase = phase
	return b
}

func (b *bindingBuilder) WithBoundAPIExport(workspaceName, exportName string) *bindingBuilder {
	b.Status.BoundAPIExport = &apisv1alpha1.ExportReference{
		Workspace: &apisv1alpha1.WorkspaceExportReference{
			WorkspaceName: workspaceName,
			ExportName:    exportName,
		},
	}
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
