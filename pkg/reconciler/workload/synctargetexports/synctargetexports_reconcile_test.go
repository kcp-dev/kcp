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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestSyncTargetExportReconcile(t *testing.T) {
	tests := []struct {
		name       string
		syncTarget *workloadv1alpha1.SyncTarget
		export     *apisv1alpha1.APIExport
		schemas    []*apisv1alpha1.APIResourceSchema

		wantError           bool
		wantSyncedResources []workloadv1alpha1.ResourceToSync
	}{
		{
			name: "export not found",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					Export: "kubernetes",
				},
			}, nil),
		},
		{
			name: "resource schemas not found",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					Export: "kubernetes",
				},
			}, nil),
			export: newAPIExport("kubernetes", []string{"v1.service"}, ""),
		},
		{
			name: "update status correctly",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					Export: "kubernetes",
				},
			}, nil),
			export: newAPIExport("kubernetes", []string{"v1.service", "apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{{Name: "v1", Served: true}}),
				newResourceSchema("v1.service", "", "services", []apisv1alpha1.APIResourceVersion{{Name: "v1", Served: true}}),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}},
				{GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "services"}, Versions: []string{"v1"}},
			},
		},
		{
			name: "update existing",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					Export: "kubernetes",
				}},
				[]workloadv1alpha1.ResourceToSync{
					{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
					{GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "services"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaPendingState},
				},
			),
			export: newAPIExport("kubernetes", []string{"v1.pod", "apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{{Name: "v1", Served: true}}),
				newResourceSchema("v1.pod", "", "pods", []apisv1alpha1.APIResourceVersion{{Name: "v1", Served: true}}),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1"}, State: workloadv1alpha1.ResourceSchemaAcceptedState},
				{GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "pods"}, Versions: []string{"v1"}},
			},
		},
		{
			name: "multiple versions",
			syncTarget: newSyncTarget([]tenancyv1alpha1.APIExportReference{
				{
					Export: "kubernetes",
				}},
				nil,
			),
			export: newAPIExport("kubernetes", []string{"apps.v1.deployment"}, ""),
			schemas: []*apisv1alpha1.APIResourceSchema{
				newResourceSchema("apps.v1.deployment", "apps", "deployments", []apisv1alpha1.APIResourceVersion{
					{Name: "v1", Served: true},
					{Name: "v1alpha1", Served: false},
					{Name: "v1beta1", Served: true},
				}),
			},
			wantSyncedResources: []workloadv1alpha1.ResourceToSync{
				{GroupResource: apisv1alpha1.GroupResource{Group: "apps", Resource: "deployments"}, Versions: []string{"v1", "v1beta1"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			getAPIExport := func(path logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
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

			reconciler := &exportReconciler{
				getAPIExport:      getAPIExport,
				getResourceSchema: getResourceSchema,
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

func newSyncTarget(exports []tenancyv1alpha1.APIExportReference, syncedResource []workloadv1alpha1.ResourceToSync) *workloadv1alpha1.SyncTarget {
	return &workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-synctarget",
		},
		Spec: workloadv1alpha1.SyncTargetSpec{
			SupportedAPIExports: exports,
		},
		Status: workloadv1alpha1.SyncTargetStatus{
			SyncedResources: syncedResource,
		},
	}
}

func newAPIExport(name string, schemas []string, identityHash string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: schemas,
		},
		Status: apisv1alpha1.APIExportStatus{
			IdentityHash: identityHash,
		},
	}
}

func newResourceSchema(name, group, resource string, versions []apisv1alpha1.APIResourceVersion) *apisv1alpha1.APIResourceSchema {
	schema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: resource,
			},
			Versions: versions,
		},
	}
	return schema
}
