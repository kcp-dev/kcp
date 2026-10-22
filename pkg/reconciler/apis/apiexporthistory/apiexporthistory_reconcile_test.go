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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestMergeScopes(t *testing.T) {
	t.Parallel()

	recorded := []adminv1alpha1.ResourceHistory{
		{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
	}

	for _, tc := range []struct {
		name        string
		observed    []adminv1alpha1.ResourceHistory
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
			observed: []adminv1alpha1.ResourceHistory{
				{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
			},
			wantLen: 1,
		},
		{
			name: "a recorded scope is never replaced",
			observed: []adminv1alpha1.ResourceHistory{
				{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.ClusterScoped},
			},
			wantLen: 1,
		},
		{
			name: "new resource is appended",
			observed: []adminv1alpha1.ResourceHistory{
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

		var created *adminv1alpha1.APIExportHistory
		var updated *adminv1alpha1.APIExportHistory

		c := &controller{
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return resourceSchema, nil
			},
			getHistory: func(string) (*adminv1alpha1.APIExportHistory, error) {
				return nil, apierrors.NewNotFound(schema.GroupResource{}, "")
			},
			createHistory: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				created = history
				return history, nil
			},
			updateHistoryStatus: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				updated = history
				return history, nil
			},
		}

		require.NoError(t, c.reconcile(context.Background(), apiExport))
		require.NotNil(t, created)
		require.Equal(t, HistoryName(cluster, "widgets"), created.Name)
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
			getHistory: func(string) (*adminv1alpha1.APIExportHistory, error) {
				return &adminv1alpha1.APIExportHistory{
					ObjectMeta: metav1.ObjectMeta{
						Name:        HistoryName(cluster, "widgets"),
						Annotations: map[string]string{adminv1alpha1.APIExportUIDAnnotationKey: "export-uid"},
					},
					Status: adminv1alpha1.APIExportHistoryStatus{
						Resources: []adminv1alpha1.ResourceHistory{
							{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
						},
					},
				}, nil
			},
			updateHistoryStatus: func(context.Context, *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				t.Fatal("unexpected status update")
				return nil, nil
			},
		}

		require.NoError(t, c.reconcile(context.Background(), apiExport))
	})

	t.Run("a failed status write is retried as stale", func(t *testing.T) {
		t.Parallel()

		stored := &adminv1alpha1.APIExportHistory{
			ObjectMeta: metav1.ObjectMeta{
				Name:        HistoryName(cluster, "widgets"),
				Annotations: map[string]string{adminv1alpha1.APIExportUIDAnnotationKey: "stale-uid"},
			},
			Status: adminv1alpha1.APIExportHistoryStatus{
				Resources: []adminv1alpha1.ResourceHistory{
					{Group: "acme.io", Resource: "gadgets", Scope: apiextensionsv1.ClusterScoped},
				},
			},
		}

		failStatus := true
		c := &controller{
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return resourceSchema, nil
			},
			getHistory: func(string) (*adminv1alpha1.APIExportHistory, error) {
				return stored.DeepCopy(), nil
			},
			updateHistory: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				stored = history.DeepCopy()
				return stored.DeepCopy(), nil
			},
			updateHistoryStatus: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				if failStatus {
					return nil, apierrors.NewInternalError(errors.New("boom"))
				}
				stored.Status = history.Status
				return stored.DeepCopy(), nil
			},
		}

		require.Error(t, c.reconcile(context.Background(), apiExport))
		require.Equal(t, "stale-uid", stored.Annotations[adminv1alpha1.APIExportUIDAnnotationKey])

		failStatus = false
		require.NoError(t, c.reconcile(context.Background(), apiExport))
		require.Equal(t, "export-uid", stored.Annotations[adminv1alpha1.APIExportUIDAnnotationKey])
		require.Len(t, stored.Status.Resources, 1)
		require.Equal(t, "widgets", stored.Status.Resources[0].Resource)
		require.Equal(t, apiextensionsv1.NamespaceScoped, stored.Status.Resources[0].Scope)
	})

	t.Run("a history left over from a deleted APIExport is replaced", func(t *testing.T) {
		t.Parallel()

		var rebound *adminv1alpha1.APIExportHistory
		var updated *adminv1alpha1.APIExportHistory

		c := &controller{
			getAPIResourceSchema: func(logicalcluster.Name, string) (*apisv1alpha1.APIResourceSchema, error) {
				return resourceSchema, nil
			},
			getHistory: func(string) (*adminv1alpha1.APIExportHistory, error) {
				return &adminv1alpha1.APIExportHistory{
					ObjectMeta: metav1.ObjectMeta{
						Name:        HistoryName(cluster, "widgets"),
						Annotations: map[string]string{adminv1alpha1.APIExportUIDAnnotationKey: "stale-uid"},
					},
					Status: adminv1alpha1.APIExportHistoryStatus{
						Resources: []adminv1alpha1.ResourceHistory{
							{Group: "acme.io", Resource: "gadgets", Scope: apiextensionsv1.ClusterScoped},
						},
					},
				}, nil
			},
			updateHistory: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				rebound = history
				return history, nil
			},
			updateHistoryStatus: func(_ context.Context, history *adminv1alpha1.APIExportHistory) (*adminv1alpha1.APIExportHistory, error) {
				updated = history
				return history, nil
			},
		}

		require.NoError(t, c.reconcile(context.Background(), apiExport))
		require.NotNil(t, rebound)
		require.Equal(t, "export-uid", rebound.Annotations[adminv1alpha1.APIExportUIDAnnotationKey])
		require.NotNil(t, updated)
		require.Len(t, updated.Status.Resources, 1)
		require.Equal(t, "widgets", updated.Status.Resources[0].Resource)
		require.Equal(t, apiextensionsv1.NamespaceScoped, updated.Status.Resources[0].Scope)
	})
}
