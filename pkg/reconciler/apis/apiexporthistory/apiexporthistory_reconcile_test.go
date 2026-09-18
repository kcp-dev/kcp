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

package apiexporthistory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestMergeScopes(t *testing.T) {
	t.Parallel()

	recorded := []apisv1alpha2.ResourceHistory{
		{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
	}

	for _, tc := range []struct {
		name        string
		observed    []apisv1alpha2.ResourceHistory
		wantChanged bool
		wantLen     int
	}{
		{
			name:     "nothing observed",
			observed: nil,
			wantLen:  1,
		},
		{
			name: "already recorded",
			observed: []apisv1alpha2.ResourceHistory{
				{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
			},
			wantLen: 1,
		},
		{
			name: "a recorded scope is never replaced",
			observed: []apisv1alpha2.ResourceHistory{
				{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.ClusterScoped},
			},
			wantLen: 1,
		},
		{
			name: "new resource is appended",
			observed: []apisv1alpha2.ResourceHistory{
				{Group: "acme.io", Resource: "gadgets", Scope: apiextensionsv1.ClusterScoped},
			},
			wantChanged: true,
			wantLen:     2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			merged, changed := mergeScopes(recorded, tc.observed)
			require.Equal(t, tc.wantChanged, changed)
			require.Len(t, merged, tc.wantLen)
			for _, resource := range merged {
				if resource.Resource == "widgets" {
					require.Equal(t, apiextensionsv1.NamespaceScoped, resource.Scope)
				}
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	cluster := logicalcluster.Name("root:acme")

	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "widgets",
			UID:         "export-uid",
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{Name: "widgets", Group: "acme.io", Schema: "v1.widgets.acme.io", Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}},
			},
		},
	}

	resourceSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "v1.widgets.acme.io",
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "acme.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets"},
			Scope: apiextensionsv1.NamespaceScoped,
		},
	}

	t.Run("creates a history and records the scope", func(t *testing.T) {
		t.Parallel()

		var created *apisv1alpha2.APIExportHistory
		var updated *apisv1alpha2.APIExportHistory

		c := &controller{
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return resourceSchema, nil
			},
			getHistory: func(string) (*apisv1alpha2.APIExportHistory, error) {
				return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
			},
			createHistory: func(_ context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error) {
				created = history
				return history, nil
			},
			updateHistoryStatus: func(_ context.Context, history *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error) {
				updated = history
				return history, nil
			},
		}

		require.NoError(t, c.reconcile(context.Background(), apiExport))
		require.NotNil(t, created)
		require.Equal(t, "export-uid", created.Name)
		require.Equal(t, cluster.String(), created.Spec.APIExport.Cluster)
		require.NotNil(t, updated)
		require.Len(t, updated.Status.Resources, 1)
		require.Equal(t, apiextensionsv1.NamespaceScoped, updated.Status.Resources[0].Scope)
	})

	t.Run("does not update when the scope is already recorded", func(t *testing.T) {
		t.Parallel()

		c := &controller{
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return resourceSchema, nil
			},
			getHistory: func(string) (*apisv1alpha2.APIExportHistory, error) {
				return &apisv1alpha2.APIExportHistory{
					ObjectMeta: metav1.ObjectMeta{Name: "export-uid"},
					Status: apisv1alpha2.APIExportHistoryStatus{
						Resources: []apisv1alpha2.ResourceHistory{
							{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
						},
					},
				}, nil
			},
			updateHistoryStatus: func(context.Context, *apisv1alpha2.APIExportHistory) (*apisv1alpha2.APIExportHistory, error) {
				t.Fatal("unexpected status update")
				return nil, nil
			},
		}

		require.NoError(t, c.reconcile(context.Background(), apiExport))
	})
}
