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

package builder

import (
	"context"
	"fmt"

	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

// NewSchemaRestProvider creates a REST provider function that serves APIResourceSchemas
// from the lister, filtered to only include schemas in the schemaClusterAndNames map.
func NewSchemaRestProvider(
	lister apisv1alpha1listers.APIResourceSchemaClusterLister,
	schemaClusterAndNames map[string]logicalcluster.Name,
) apiserver.RestProviderFunc {
	return func(
		resource schema.GroupVersionResource,
		kind schema.GroupVersionKind,
		listKind schema.GroupVersionKind,
		typer runtime.ObjectTyper,
		tableConvertor rest.TableConvertor,
		namespaceScoped bool,
		schemaValidator validation.SchemaValidator,
		subresourcesSchemaValidator map[string]validation.SchemaValidator,
		structuralSchema *structuralschema.Structural,
	) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		// Create the storage functions
		factoryFunc := forwardingregistry.FactoryFunc(func() runtime.Object {
			ret := &unstructured.Unstructured{}
			ret.SetGroupVersionKind(kind)
			return ret
		})

		listFactoryFunc := forwardingregistry.ListFactoryFunc(func() runtime.Object {
			ret := &unstructured.UnstructuredList{}
			ret.SetGroupVersionKind(listKind)
			return ret
		})

		destroyerFunc := forwardingregistry.DestroyerFunc(func() {})

		getterFunc := forwardingregistry.GetterFunc(func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			clusterName, ok := schemaClusterAndNames[name]
			if !ok {
				return nil, errors.NewNotFound(resource.GroupResource(), name)
			}

			apiSchema, err := lister.Cluster(clusterName).Get(name)
			if err != nil {
				return nil, err
			}

			// Convert to unstructured
			unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(apiSchema)
			if err != nil {
				return nil, fmt.Errorf("failed to convert schema to unstructured: %w", err)
			}

			result := &unstructured.Unstructured{Object: unstructuredObj}
			result.SetGroupVersionKind(kind)
			return result, nil
		})

		listerFunc := forwardingregistry.ListerFunc(func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			items := make([]unstructured.Unstructured, 0, len(schemaClusterAndNames))

			for schemaName, clusterName := range schemaClusterAndNames {
				apiSchema, err := lister.Cluster(clusterName).Get(schemaName)
				if err != nil {
					// Skip schemas that can't be found (they may have been deleted)
					if errors.IsNotFound(err) {
						continue
					}
					return nil, err
				}

				unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(apiSchema)
				if err != nil {
					return nil, fmt.Errorf("failed to convert schema %s to unstructured: %w", schemaName, err)
				}

				item := unstructured.Unstructured{Object: unstructuredObj}
				item.SetGroupVersionKind(kind)
				items = append(items, item)
			}

			list := &unstructured.UnstructuredList{Items: items}
			list.SetGroupVersionKind(listKind)
			return list, nil
		})

		watcherFunc := forwardingregistry.WatcherFunc(func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			// For now, return an empty watch that never sends events.
			// A full implementation would need to watch the underlying schemas.
			return watch.NewEmptyWatch(), nil
		})

		tableConvertorFunc := forwardingregistry.TableConvertorFunc(func(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
			if tableConvertor != nil {
				return tableConvertor.ConvertToTable(ctx, object, tableOptions)
			}
			return rest.NewDefaultTableConvertor(resource.GroupResource()).ConvertToTable(ctx, object, tableOptions)
		})

		categoriesProviderFunc := forwardingregistry.CategoriesProviderFunc(func() []string {
			return nil
		})

		resetFieldsStrategyFunc := forwardingregistry.ResetFieldsStrategyFunc(func() map[fieldpath.APIVersion]*fieldpath.Set {
			return nil
		})

		// Return the storage struct matching what ProvideReadOnlyRestStorage returns
		return &struct {
			forwardingregistry.FactoryFunc
			forwardingregistry.ListFactoryFunc
			forwardingregistry.DestroyerFunc

			forwardingregistry.GetterFunc
			forwardingregistry.ListerFunc
			forwardingregistry.WatcherFunc

			forwardingregistry.TableConvertorFunc
			forwardingregistry.CategoriesProviderFunc
			forwardingregistry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     factoryFunc,
			ListFactoryFunc: listFactoryFunc,
			DestroyerFunc:   destroyerFunc,

			GetterFunc:  getterFunc,
			ListerFunc:  listerFunc,
			WatcherFunc: watcherFunc,

			TableConvertorFunc:      tableConvertorFunc,
			CategoriesProviderFunc:  categoriesProviderFunc,
			ResetFieldsStrategyFunc: resetFieldsStrategyFunc,
		}, nil
	}
}
