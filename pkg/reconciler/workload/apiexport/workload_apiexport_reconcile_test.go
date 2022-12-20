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

package apiexport

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/config/rootcompute"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type SchemaCheck func(t *testing.T, s *apisv1alpha1.APIResourceSchema)

func someSchemaWeDontCareAboutInDetail(t *testing.T, got *apisv1alpha1.APIResourceSchema) {}

func equals(expected *apisv1alpha1.APIResourceSchema) func(*testing.T, *apisv1alpha1.APIResourceSchema) {
	return func(t *testing.T, got *apisv1alpha1.APIResourceSchema) {
		t.Helper()
		require.Equal(t, expected, got)
	}
}

type ExportCheck func(t *testing.T, s *apisv1alpha1.APIExport)

func hasSchemas(expected ...string) func(*testing.T, *apisv1alpha1.APIExport) {
	return func(t *testing.T, got *apisv1alpha1.APIExport) {
		t.Helper()
		require.Equal(t, expected, got.Spec.LatestResourceSchemas)
	}
}

func TestSchemaReconciler(t *testing.T) {
	tests := map[string]struct {
		negotiatedResources map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource
		schemas             map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema
		syncTargets         map[logicalcluster.Name][]*workloadv1alpha1.SyncTarget
		export              *apisv1alpha1.APIExport

		listNegotiatedAPIResourcesError error
		listAPIResourceSchemaError      error
		getAPIResourceSchemaError       error
		createAPIResourceSchemaError    error
		deleteAPIResourceSchemaError    error
		updateAPIExportError            error

		wantSchemaCreates map[string]SchemaCheck
		wantExportUpdates map[string]ExportCheck
		wantSchemaDeletes map[string]struct{}

		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"some other export": {
			export:              export(logicalcluster.NewPath("root:org:ws"), "test"),
			wantReconcileStatus: reconcileStatusStop,
		},
		"no negotiated API resources": {
			export:              export(logicalcluster.NewPath("root:org:ws"), "kubernetes"),
			wantReconcileStatus: reconcileStatusStop,
		},
		"dangling schema, but no negotiated API resources": {
			export:              export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-43.deployments.apps"),
			wantReconcileStatus: reconcileStatusStop,
		},
		"negotiated API resource, but some other export": {
			export: export(logicalcluster.NewPath("root:org:ws"), "something", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"), "kubernetes"), // older RV
				},
			},
			wantReconcileStatus: reconcileStatusStop,
		},
		"full api resource": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "kubernetes"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "deployments.v1.apps",
							ResourceVersion: "52",
						},
						Spec: apiresourcev1alpha1.NegotiatedAPIResourceSpec{
							CommonAPIResourceSpec: apiresourcev1alpha1.CommonAPIResourceSpec{
								GroupVersion: apiresourcev1alpha1.GroupVersion{Group: "apps", Version: "v1"},
								Scope:        "Namespaced",
								CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
									Plural:   "deployments",
									Singular: "deployment",
									Kind:     "Deployment",
									ListKind: "DeploymentList",
								},
								OpenAPIV3Schema: runtime.RawExtension{
									Raw: []byte(`{
										"type": "object",
										"properties": {
											"spec": {
												"type": "object"
											}
										}
									}`),
								},
								SubResources: apiresourcev1alpha1.SubResources{
									{Name: "scale"},
									{Name: "status"},
								},
								ColumnDefinitions: apiresourcev1alpha1.ColumnDefinitions{
									{
										TableColumnDefinition: metav1.TableColumnDefinition{
											Name:        "replicas",
											Type:        "number",
											Description: "Number of replicas",
											Priority:    0,
										},
										JSONPath: pointer.StringPtr(".status.replicas"),
									},
									{
										TableColumnDefinition: metav1.TableColumnDefinition{
											Name:        "available",
											Type:        "number",
											Description: "Number of available replicas",
											Priority:    0,
										},
										JSONPath: pointer.StringPtr(".status.availableReplicas"),
									},
								},
							},
							Publish: false,
						},
					},
				},
			},
			wantSchemaCreates: map[string]SchemaCheck{
				"rev-52.deployments.apps": equals(&apisv1alpha1.APIResourceSchema{
					ObjectMeta: metav1.ObjectMeta{
						Name: "rev-52.deployments.apps",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion:         "apis.kcp.dev/v1alpha1",
								Kind:               "APIExport",
								Name:               "kubernetes",
								Controller:         pointer.BoolPtr(true),
								BlockOwnerDeletion: pointer.BoolPtr(true),
							},
						},
					},
					Spec: apisv1alpha1.APIResourceSchemaSpec{
						Group: "apps",
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Plural:   "deployments",
							Singular: "deployment",
							Kind:     "Deployment",
							ListKind: "DeploymentList",
						},
						Scope: "Namespaced",
						Versions: []apisv1alpha1.APIResourceVersion{
							{
								Name:    "v1",
								Served:  true,
								Storage: true,
								Schema: runtime.RawExtension{
									Raw: []byte(`{
										"type": "object",
										"properties": {
											"spec": {
												"type": "object"
											}
										}
									}`),
								},
								Subresources: apiextensionsv1.CustomResourceSubresources{
									Scale: &apiextensionsv1.CustomResourceSubresourceScale{
										SpecReplicasPath:   ".spec.replicas",
										StatusReplicasPath: ".status.replicas",
									},
									Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
								},
								AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
									{
										Name:        "replicas",
										Type:        "number",
										Description: "Number of replicas",
										Priority:    0,
										JSONPath:    ".status.replicas",
									},
									{
										Name:        "available",
										Type:        "number",
										Description: "Number of available replicas",
										Priority:    0,
										JSONPath:    ".status.availableReplicas",
									},
								},
							},
						},
					},
				}),
			},
			wantExportUpdates: map[string]ExportCheck{
				"kubernetes": hasSchemas("rev-52.deployments.apps"),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"non-triple schema name": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "foo"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			wantSchemaCreates: map[string]SchemaCheck{
				"rev-15.services.core": someSchemaWeDontCareAboutInDetail,
			},
			wantExportUpdates: map[string]ExportCheck{
				"kubernetes": hasSchemas("rev-15.services.core"),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"dangling schema in export": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-43.deployments.apps"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-43", "apps", "v1", "Deployment"), "kubernetes"),
				},
			},
			wantSchemaCreates: map[string]SchemaCheck{
				"rev-15.services.core": someSchemaWeDontCareAboutInDetail,
			},
			wantExportUpdates: map[string]ExportCheck{
				"kubernetes": hasSchemas("rev-15.services.core"),
			},
			wantSchemaDeletes:   map[string]struct{}{"rev-43.deployments.apps": {}},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"up-to-date schema": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"), "kubernetes"), // older RV
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"outdated schema": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withDifferentOpenAPI(
						withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"), "kubernetes"),
						`{"type":"object"}`,
					),
				},
			},
			wantSchemaCreates: map[string]SchemaCheck{
				"rev-15.services.core": someSchemaWeDontCareAboutInDetail,
			},
			wantExportUpdates: map[string]ExportCheck{
				"kubernetes": hasSchemas("rev-15.services.core"),
			},
			wantSchemaDeletes:   map[string]struct{}{"rev-10.services.core": {}},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"unused schemas": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"), "kubernetes"),        // older RV
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-19", "apps", "v1", "Deployment"), "kubernetes"), // older RV
					apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"),                                       // not owned by export
				},
			},
			wantSchemaDeletes:   map[string]struct{}{"rev-19.deployments.apps": {}},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"skip kubernetes schema": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			syncTargets: map[logicalcluster.Name][]*workloadv1alpha1.SyncTarget{
				"root:org:ws": {
					syncTarget("syncTarget1", rootcompute.RootComputeClusterName, "kubernetes"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-15", "", "v1", "Service"), "kubernetes"),
				},
			},
			wantSchemaDeletes: map[string]struct{}{"rev-15.services.core": {}},
			wantExportUpdates: map[string]ExportCheck{
				"kubernetes": hasSchemas([]string{}...),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"keep local kubernetes schema": {
			export: export(logicalcluster.NewPath("root:org:ws"), "kubernetes", "rev-10.services.core"),
			negotiatedResources: map[logicalcluster.Name][]*apiresourcev1alpha1.NegotiatedAPIResource{
				"root:org:ws": {
					negotiatedAPIResource(logicalcluster.NewPath("root:org:ws"), "core", "v1", "Service"),
				},
			},
			syncTargets: map[logicalcluster.Name][]*workloadv1alpha1.SyncTarget{
				"root:org:ws": {
					syncTarget("syncTarget1", logicalcluster.NewPath("root:org:ws"), "other-export"),
				},
			},
			schemas: map[logicalcluster.Name][]*apisv1alpha1.APIResourceSchema{
				"root:org:ws": {
					withExportOwner(apiResourceSchema(logicalcluster.NewPath("root:org:ws"), "rev-10", "", "v1", "Service"), "kubernetes"), // older RV
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			schemaCreates := map[string]*apisv1alpha1.APIResourceSchema{}
			exportUpdates := map[string]*apisv1alpha1.APIExport{}
			schemeDeletes := map[string]struct{}{}
			r := &schemaReconciler{
				listNegotiatedAPIResources: func(clusterName logicalcluster.Name) ([]*apiresourcev1alpha1.NegotiatedAPIResource, error) {
					if tc.listNegotiatedAPIResourcesError != nil {
						return nil, tc.listNegotiatedAPIResourcesError
					}
					return tc.negotiatedResources[clusterName], nil
				},
				listSyncTargets: func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
					return tc.syncTargets[clusterName], nil
				},
				listAPIResourceSchemas: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIResourceSchema, error) {
					if tc.listAPIResourceSchemaError != nil {
						return nil, tc.listAPIResourceSchemaError
					}
					return tc.schemas[clusterName], nil
				},
				getAPIResourceSchema: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					if tc.getAPIResourceSchemaError != nil {
						return nil, tc.getAPIResourceSchemaError
					}
					for _, s := range tc.schemas[clusterName] {
						if s.Name == name {
							return s, nil
						}
					}
					return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apis.kcp.dev", Resource: "apiresourceschemas"}, name)
				},
				createAPIResourceSchema: func(ctx context.Context, clusterName logicalcluster.Path, schema *apisv1alpha1.APIResourceSchema) (*apisv1alpha1.APIResourceSchema, error) {
					if tc.createAPIResourceSchemaError != nil {
						return nil, tc.createAPIResourceSchemaError
					}
					schemaCreates[schema.Name] = schema.DeepCopy()
					return schema, nil
				},
				updateAPIExport: func(ctx context.Context, clusterName logicalcluster.Path, export *apisv1alpha1.APIExport) (*apisv1alpha1.APIExport, error) {
					if tc.updateAPIExportError != nil {
						return nil, tc.updateAPIExportError
					}
					exportUpdates[export.Name] = export.DeepCopy()
					return export, nil
				},
				deleteAPIResourceSchema: func(ctx context.Context, clusterName logicalcluster.Path, name string) error {
					if tc.deleteAPIResourceSchemaError != nil {
						return tc.deleteAPIResourceSchemaError
					}
					schemeDeletes[name] = struct{}{}
					return nil
				},
				enqueueAfter: func(export *apisv1alpha1.APIExport, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, err := r.reconcile(context.Background(), tc.export)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)

			// check creates
			for _, s := range schemaCreates {
				require.Contains(t, tc.wantSchemaCreates, s.Name, "got unexpected create:\n%s", toYaml(s))
				tc.wantSchemaCreates[s.Name](t, s)
			}
			for name := range tc.wantSchemaCreates {
				require.Contains(t, schemaCreates, name, "missing create of %s", name)
			}

			// check updates
			for _, e := range exportUpdates {
				require.Contains(t, tc.wantExportUpdates, e.Name, "got unexpected update:\n%s", toYaml(e))
				tc.wantExportUpdates[e.Name](t, e)
			}
			for name := range tc.wantExportUpdates {
				require.Contains(t, exportUpdates, name, "missing update for %s", name)
			}

			// check deletes
			for name := range schemeDeletes {
				require.Contains(t, tc.wantSchemaDeletes, name, "got unexpected delete of %q", name)
			}
			for name := range tc.wantSchemaDeletes {
				require.Contains(t, schemeDeletes, name, "missing delete of %q", name)
			}
		})
	}
}

func export(clusterName logicalcluster.Path, name string, exports ...string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName.String(),
			},
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: exports,
		},
	}
}

func apiResourceSchema(clusterName logicalcluster.Path, prefix string, group string, version string, kind string) *apisv1alpha1.APIResourceSchema {
	nameGroup := group
	if nameGroup == "" {
		nameGroup = "core"
	}
	return &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%ss.%s", prefix, strings.ToLower(kind), nameGroup),
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName.String(),
			},
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   strings.ToLower(kind) + "s",
				Singular: strings.ToLower(kind),
				Kind:     kind,
				ListKind: kind + "List",
			},
			Scope: "Namespaced",
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    version,
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`{
										"type": "object",
										"properties": {
											"spec": {
												"type": "object"
											}
										}
									}`),
					},
				},
			},
		},
	}
}

func withExportOwner(schema *apisv1alpha1.APIResourceSchema, exportName string) *apisv1alpha1.APIResourceSchema {
	schema.OwnerReferences = append(schema.OwnerReferences, metav1.OwnerReference{
		APIVersion:         apisv1alpha1.SchemeGroupVersion.String(),
		Kind:               "APIExport",
		Name:               exportName,
		Controller:         pointer.BoolPtr(true),
		BlockOwnerDeletion: pointer.BoolPtr(true),
	})
	return schema
}

func withDifferentOpenAPI(schema *apisv1alpha1.APIResourceSchema, openAPISchema string) *apisv1alpha1.APIResourceSchema {
	schema.Spec.Versions[0].Schema.Raw = []byte(openAPISchema)
	return schema
}

func negotiatedAPIResource(clusterName logicalcluster.Path, group string, version string, kind string) *apiresourcev1alpha1.NegotiatedAPIResource {
	return &apiresourcev1alpha1.NegotiatedAPIResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:            fmt.Sprintf("%ss.%s.%s", strings.ToLower(kind), version, group),
			ResourceVersion: "15",
		},
		Spec: apiresourcev1alpha1.NegotiatedAPIResourceSpec{
			CommonAPIResourceSpec: apiresourcev1alpha1.CommonAPIResourceSpec{
				GroupVersion: apiresourcev1alpha1.GroupVersion{Group: group, Version: version},
				Scope:        "Namespaced",
				CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   strings.ToLower(kind) + "s",
					Singular: strings.ToLower(kind),
					Kind:     kind,
					ListKind: kind + "List",
				},
				OpenAPIV3Schema: runtime.RawExtension{
					Raw: []byte(`{
										"type": "object",
										"properties": {
											"spec": {
												"type": "object"
											}
										}
									}`),
				},
			},
			Publish: false,
		},
	}
}

func syncTarget(syncTargetName string, computeWorkspace logicalcluster.Path, exportName string) *workloadv1alpha1.SyncTarget {
	return &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: syncTargetName,
		},
		Spec: workloadv1alpha1.SyncTargetSpec{
			SupportedAPIExports: []tenancyv1alpha1.APIExportReference{
				{
					Path:   computeWorkspace.String(),
					Export: exportName,
				},
			},
		},
	}
}

func toYaml(obj interface{}) string {
	bytes, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}
