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

package synctargetexports

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestSyncTargetCompatibleReconcile(t *testing.T) {
	tests := []struct {
		name              string
		syncTarget        *workloadv1alpha1.SyncTarget
		export            *apisv1alpha1.APIExport
		schemas           []*apisv1alpha1.APIResourceSchema
		apiResourceImport []*apiresourcev1alpha1.APIResourceImport

		wantError           bool
		wantSyncedResources []workloadv1alpha1.ResourceToSync
	}{
		{
			name: "pending when missing APIResourceSchema",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					ExportName: "kubernetes",
				},
			},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
				},
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaPendingState},
			},
		},
		{
			name: "incompatible when missing APIResourceImport",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					ExportName: "kubernetes",
				},
			},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
				},
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{
						Name:   "v1",
						Served: true,
						Schema: runtime.RawExtension{Raw: []byte(`{"type":"string"}`)},
					},
				}),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaIncompatibleState},
			},
		},
		{
			name: "APIResourceImport compatible with APIResourceSchema",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					ExportName: "kubernetes",
				},
			},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaPendingState},
				},
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{
						Name:   "v1",
						Served: true,
						Schema: runtime.RawExtension{Raw: []byte(`{"type":"string"}`)},
					},
				}),
			},
			apiResourceImport: []*apiresourcev1alpha1.APIResourceImport{
				newAPIResourceImport("apps.v1.deployment", "apps", "deployments", "v1", `{"type":"string"}`),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
			},
		},
		{
			name: "APIResourceImport incompatible with APIResourceSchema",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					ExportName: "kubernetes",
				},
			},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
				},
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{
						Name:   "v1",
						Served: true,
						Schema: runtime.RawExtension{Raw: []byte(`{"type":"integer"}`)},
					},
				}),
			},
			apiResourceImport: []*apiresourcev1alpha1.APIResourceImport{
				newAPIResourceImport("apps.v1.deployment", "apps", "deployments", "v1", `{"type":"string"}`),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaIncompatibleState},
			},
		},
		{
			name: "only take care latest version",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					ExportName: "kubernetes",
				},
			},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1", "v1beta1"}, State: workloadv1alpha1.ResourceSchemaPendingState},
				},
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{
						Name:   "v1",
						Served: true,
						Schema: runtime.RawExtension{Raw: []byte(`{"type":"string"}`)},
					},
				}),
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{
						Name:   "v1beta1",
						Served: true,
						Schema: runtime.RawExtension{Raw: []byte(`{"type":"string"}`)},
					},
				}),
			},
			apiResourceImport: []*apiresourcev1alpha1.APIResourceImport{
				newAPIResourceImport("apps.v1.deployment", "apps", "deployments", "v1", `{"type":"string"}`),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1", "v1beta1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getAPIExport := func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
				if tc.export == nil {
					return nil, errors.NewNotFound(schema.GroupResource{}, name)
				}
				return tc.export, nil
			}
			getResourceSchema := func(clusterName tenancy.Cluster, name string) (*apisv1alpha1.APIResourceSchema, error) {
				for _, schema := range tc.schemas {
					if schema.Name == name {
						return schema, nil
					}
				}

				return nil, errors.NewNotFound(schema.GroupResource{}, name)
			}
			listAPIResourceImports := func(clusterName tenancy.Cluster) ([]*apiresourcev1alpha1.APIResourceImport, error) {
				return tc.apiResourceImport, nil
			}

			reconciler := &apiCompatibleReconciler{
				getAPIExport:           getAPIExport,
				getResourceSchema:      getResourceSchema,
				listAPIResourceImports: listAPIResourceImports,
			}

			updated, err := reconciler.reconcile(context.TODO(), tc.syncTarget)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tc.wantSyncedResources, updated.Status.SyncedResources)
		})
	}
}

func newAPIResourceImport(name, group, resource, version, schema string) *apiresourcev1alpha1.APIResourceImport {
	return &apiresourcev1alpha1.APIResourceImport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiresourcev1alpha1.APIResourceImportSpec{
			CommonAPIResourceSpec: apiresourcev1alpha1.CommonAPIResourceSpec{
				GroupVersion: apiresourcev1alpha1.GroupVersion{
					Group:   group,
					Version: version,
				},
				CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
					Plural: resource,
				},
				OpenAPIV3Schema: runtime.RawExtension{Raw: []byte(schema)},
			},
		},
	}
}
