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

package apiexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func newTestIndexer(t *testing.T, objs ...interface{}) cache.Indexer {
	t.Helper()

	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
	for _, obj := range objs {
		require.NoError(t, indexer.Add(obj))
	}
	return indexer
}

func TestValidateHistory(t *testing.T) {
	t.Parallel()

	cluster := logicalcluster.Name("root:acme")

	newSchema := func(name string, scope apiextensionsv1.ResourceScope) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "acme.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets"},
				Scope: scope,
			},
		}
	}

	history := &apisv1alpha2.APIExportHistory{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "export-uid",
			Annotations: map[string]string{logicalcluster.AnnotationKey: apibinding.SystemBoundCRDsClusterName.String()},
		},
		Spec: apisv1alpha2.APIExportHistorySpec{
			APIExport: apisv1alpha2.APIExportHistoryRef{Cluster: cluster.String(), Name: "widgets"},
		},
		Status: apisv1alpha2.APIExportHistoryStatus{
			Resources: []apisv1alpha2.ResourceHistory{
				{Group: "acme.io", Resource: "widgets", Scope: apiextensionsv1.NamespaceScoped},
			},
		},
	}

	for _, tc := range []struct {
		name      string
		uid       string
		schema    *apisv1alpha1.APIResourceSchema
		histories []interface{}
		wantErr   bool
	}{
		{
			name:      "same scope is allowed",
			uid:       "export-uid",
			schema:    newSchema("v1.widgets.acme.io", apiextensionsv1.NamespaceScoped),
			histories: []interface{}{history},
		},
		{
			name:      "a different scope is rejected",
			uid:       "export-uid",
			schema:    newSchema("v2.widgets.acme.io", apiextensionsv1.ClusterScoped),
			histories: []interface{}{history},
			wantErr:   true,
		},
		{
			name:   "no history yet",
			uid:    "export-uid",
			schema: newSchema("v1.widgets.acme.io", apiextensionsv1.ClusterScoped),
		},
		{
			name:      "a new APIExport has no history",
			schema:    newSchema("v1.widgets.acme.io", apiextensionsv1.ClusterScoped),
			histories: []interface{}{history},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })
			e.hasSynced = func() bool { return true }
			e.apiResourceSchemaLister = apisv1alpha1listers.NewAPIResourceSchemaClusterLister(newTestIndexer(t, tc.schema))
			e.historyLister = apisv1alpha2listers.NewAPIExportHistoryClusterLister(newTestIndexer(t, tc.histories...))

			apiExport := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "widgets",
					UID:         types.UID(tc.uid),
					Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{Name: "widgets", Group: "acme.io", Schema: tc.schema.Name},
					},
				},
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: cluster})
			err := e.validateHistory(ctx, updateAttr("widgets", apiExport, "APIExport", "apiexports"), apiExport)
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, apierrors.IsForbidden(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateHistoryAgainstOldAPIExport(t *testing.T) {
	t.Parallel()

	cluster := logicalcluster.Name("root:acme")

	newSchema := func(name string, scope apiextensionsv1.ResourceScope) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "acme.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets"},
				Scope: scope,
			},
		}
	}

	namespaced := newSchema("v1.widgets.acme.io", apiextensionsv1.NamespaceScoped)
	clusterScoped := newSchema("v2.widgets.acme.io", apiextensionsv1.ClusterScoped)

	newExport := func(schemaName string) *apisv1alpha2.APIExport {
		return &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "widgets",
				UID:         types.UID("export-uid"),
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
			},
			Spec: apisv1alpha2.APIExportSpec{
				Resources: []apisv1alpha2.ResourceSchema{
					{Name: "widgets", Group: "acme.io", Schema: schemaName},
				},
			},
		}
	}

	for _, tc := range []struct {
		name    string
		oldName string
		newName string
		wantErr bool
	}{
		{
			name:    "a scope change against the previous spec is rejected without a history",
			oldName: namespaced.Name,
			newName: clusterScoped.Name,
			wantErr: true,
		},
		{
			name:    "an unchanged scope is allowed",
			oldName: namespaced.Name,
			newName: namespaced.Name,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })
			e.hasSynced = func() bool { return true }
			e.apiResourceSchemaLister = apisv1alpha1listers.NewAPIResourceSchemaClusterLister(newTestIndexer(t, namespaced, clusterScoped))
			e.historyLister = apisv1alpha2listers.NewAPIExportHistoryClusterLister(newTestIndexer(t))

			attr := updateAttrWithOld("widgets", newExport(tc.newName), newExport(tc.oldName), "APIExport", "apiexports")

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: cluster})
			err := e.validateHistory(ctx, attr, newExport(tc.newName))
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, apierrors.IsForbidden(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateHistoryWhileInformersUnsynced(t *testing.T) {
	t.Parallel()

	cluster := logicalcluster.Name("root:acme")

	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "widgets",
			UID:         types.UID("export-uid"),
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
	}

	for _, tc := range []struct {
		name    string
		groups  []string
		wantErr bool
	}{
		{
			name:   "privileged bootstrap writes are exempt",
			groups: []string{kuser.SystemPrivilegedGroup},
		},
		{
			name:    "everybody else fails closed",
			groups:  []string{"system:authenticated"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := NewAPIExportAdmission(func(apis.GroupResource) bool { return false })
			e.hasSynced = func() bool { return false }

			attr := admission.NewAttributesRecord(
				helpers.ToUnstructuredOrDie(apiExport),
				helpers.ToUnstructuredOrDie(apiExport),
				apisv1alpha2.Kind("APIExport").WithVersion("v1alpha2"),
				"",
				apiExport.Name,
				apisv1alpha2.Resource("apiexports").WithVersion("v1alpha2"),
				"",
				admission.Update,
				&metav1.UpdateOptions{},
				false,
				&kuser.DefaultInfo{Name: "someone", Groups: tc.groups},
			)

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: cluster})
			err := e.validateHistory(ctx, attr, apiExport)
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, apierrors.IsForbidden(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}
