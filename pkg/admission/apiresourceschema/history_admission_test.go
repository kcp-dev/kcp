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

package apiresourceschema

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

func TestValidateHistoryOnSchemaCreate(t *testing.T) {
	t.Parallel()

	cluster := logicalcluster.Name("root:acme")

	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "widgets",
			UID:         types.UID("export-uid"),
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{Name: "widgets", Group: "acme.io", Schema: "v2.widgets.acme.io"},
			},
		},
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

	newIndexer := func(objs ...interface{}) cache.Indexer {
		indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
		for _, obj := range objs {
			require.NoError(t, indexer.Add(obj))
		}
		return indexer
	}

	for _, tc := range []struct {
		name      string
		schema    string
		scope     apiextensionsv1.ResourceScope
		histories []interface{}
		wantErr   bool
	}{
		{
			name:      "a schema flipping the recorded scope is rejected",
			schema:    "v2.widgets.acme.io",
			scope:     apiextensionsv1.ClusterScoped,
			histories: []interface{}{history},
			wantErr:   true,
		},
		{
			name:      "a schema keeping the recorded scope is allowed",
			schema:    "v2.widgets.acme.io",
			scope:     apiextensionsv1.NamespaceScoped,
			histories: []interface{}{history},
		},
		{
			name:      "a schema no APIExport references is allowed",
			schema:    "v9.widgets.acme.io",
			scope:     apiextensionsv1.ClusterScoped,
			histories: []interface{}{history},
		},
		{
			name:   "no history recorded yet",
			schema: "v2.widgets.acme.io",
			scope:  apiextensionsv1.ClusterScoped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			o := &apiResourceSchemaValidation{
				Handler:         admission.NewHandler(admission.Create, admission.Update),
				hasSynced:       func() bool { return true },
				apiExportLister: apisv1alpha2listers.NewAPIExportClusterLister(newIndexer(apiExport)),
				historyLister:   apisv1alpha2listers.NewAPIExportHistoryClusterLister(newIndexer(tc.histories...)),
			}

			schema := &apisv1alpha1.APIResourceSchema{
				ObjectMeta: metav1.ObjectMeta{
					Name:        tc.schema,
					Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
				},
				Spec: apisv1alpha1.APIResourceSchemaSpec{
					Group: "acme.io",
					Names: apiextensionsv1.CustomResourceDefinitionNames{Plural: "widgets"},
					Scope: tc.scope,
				},
			}

			attr := admission.NewAttributesRecord(
				helpers.ToUnstructuredOrDie(schema),
				nil,
				apisv1alpha1.Kind("APIResourceSchema").WithVersion("v1alpha1"),
				"",
				schema.Name,
				apisv1alpha1.Resource("apiresourceschemas").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			)

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: cluster})
			err := o.validateHistory(ctx, attr, schema)
			if tc.wantErr {
				require.Error(t, err)
				require.True(t, apierrors.IsForbidden(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}
