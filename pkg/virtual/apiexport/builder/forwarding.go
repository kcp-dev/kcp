/*
Copyright 2022 The kcp Authors.

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

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/sdk/apis/apis/v1alpha2/permissionclaims"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	registry "github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	apiexportbuiltin "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

func provideAPIExportFilteredRestStorage(ctx context.Context, dynamicClusterClientFunc registry.DynamicClusterClientFunc, clusterName logicalcluster.Name, exportName string) (apiserver.RestProviderFunc, error) {
	labelSelector := map[string]string{
		apisv1alpha1.InternalAPIBindingExportLabelKey: permissionclaims.ToAPIBindingExportLabelValue(clusterName, exportName),
	}
	requirements, selectable := labels.SelectorFromSet(labelSelector).Requirements()
	if !selectable {
		return nil, fmt.Errorf("unable to create a selector from the provided labels")
	}

	return registry.ProvideReadOnlyRestStorage(ctx, dynamicClusterClientFunc, registry.WithStaticLabelSelector(requirements), nil)
}

// provideDelegatingRestStorage returns a forwarding storage build function, with an optional storage wrapper e.g. to add label based filtering.
func provideDelegatingRestStorage(ctx context.Context, dynamicClusterClientFunc registry.DynamicClusterClientFunc, apiExportIdentityHash string, wrapper registry.StorageWrapper) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator validation.SchemaValidator, subresourcesSchemaValidator map[string]validation.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		_, scaleEnabled := subresourcesSchemaValidator["scale"]

		var scaleSpec *apiextensions.CustomResourceSubresourceScale
		if scaleEnabled {
			scaleSpec = &apiextensions.CustomResourceSubresourceScale{}
		}

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			registry.ValidatePathSegmentName,
			schemaValidator,
			statusSchemaValidate,
			structuralSchema,
			statusSpec,
			scaleSpec,
			[]apiextensionsv1.SelectableField{},
		)

		storage, statusStorage, scaleStorage := registry.NewStorage(
			ctx,
			resource,
			apiExportIdentityHash,
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			dynamicClusterClientFunc,
			nil,
			wrapper,
		)

		// we want to expose some but not all the allowed endpoints, so filter by exposing just the funcs we need
		subresourceStorages = make(map[string]rest.Storage)
		if statusEnabled {
			subresourceStorages["status"] = &struct {
				registry.FactoryFunc
				registry.DestroyerFunc

				registry.GetterFunc
				registry.UpdaterFunc
				// patch is implicit as we have get + update

				registry.TableConvertorFunc
				registry.CategoriesProviderFunc
				registry.ResetFieldsStrategyFunc
			}{
				FactoryFunc:   statusStorage.FactoryFunc,
				DestroyerFunc: statusStorage.DestroyerFunc,

				GetterFunc:  statusStorage.GetterFunc,
				UpdaterFunc: statusStorage.UpdaterFunc,

				TableConvertorFunc:      statusStorage.TableConvertorFunc,
				CategoriesProviderFunc:  statusStorage.CategoriesProviderFunc,
				ResetFieldsStrategyFunc: statusStorage.ResetFieldsStrategyFunc,
			}
		}

		if scaleEnabled {
			subresourceStorages["scale"] = &struct {
				registry.FactoryFunc
				registry.DestroyerFunc

				registry.GetterFunc
				registry.UpdaterFunc
				// patch is implicit as we have get + update

				registry.TableConvertorFunc
				registry.CategoriesProviderFunc
				registry.ResetFieldsStrategyFunc
			}{
				FactoryFunc:   scaleStorage.FactoryFunc,
				DestroyerFunc: scaleStorage.DestroyerFunc,

				GetterFunc:  scaleStorage.GetterFunc,
				UpdaterFunc: scaleStorage.UpdaterFunc,

				TableConvertorFunc:      scaleStorage.TableConvertorFunc,
				CategoriesProviderFunc:  scaleStorage.CategoriesProviderFunc,
				ResetFieldsStrategyFunc: scaleStorage.ResetFieldsStrategyFunc,
			}
		}

		for name, subresourceGVK := range apiexportbuiltin.BuiltInSubresources[resource.GroupResource()] {
			factory := func() runtime.Object {
				ret := &unstructured.Unstructured{}
				ret.SetGroupVersionKind(subresourceGVK)
				return ret
			}
			subresourceStore := registry.DefaultDynamicDelegatedStoreFuncs(
				factory,
				nil,
				func() {},
				strategy,
				tableConvertor,
				resource,
				apiExportIdentityHash,
				nil,
				dynamicClusterClientFunc,
				[]string{name},
				retry.DefaultRetry,
				ctx.Done(),
			)

			// get the parent resource for the subresource so the parents' permissions are validated.
			// prevents e.g. accessing the subresource of an unclaimed parent resource.
			delegateCreate := subresourceStore.NamedCreaterFunc
			subresourceStore.NamedCreaterFunc = func(ctx context.Context, name string, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
				if _, err := storage.GetterFunc.Get(ctx, name, &metav1.GetOptions{}); err != nil {
					return nil, err
				}
				return delegateCreate(ctx, name, obj, createValidation, options)
			}

			subresourceStorages[name] = &struct {
				registry.FactoryFunc
				registry.DestroyerFunc

				registry.NamedCreaterFunc
			}{
				FactoryFunc:   subresourceStore.FactoryFunc,
				DestroyerFunc: subresourceStore.DestroyerFunc,

				NamedCreaterFunc: subresourceStore.NamedCreaterFunc,
			}
		}

		return &struct {
			registry.FactoryFunc
			registry.ListFactoryFunc
			registry.DestroyerFunc

			registry.GetterFunc
			registry.ListerFunc
			registry.UpdaterFunc
			registry.WatcherFunc
			registry.CreaterFunc
			registry.CollectionDeleterFunc
			registry.GracefulDeleterFunc

			registry.TableConvertorFunc
			registry.CategoriesProviderFunc
			registry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:            storage.GetterFunc,
			ListerFunc:            storage.ListerFunc,
			UpdaterFunc:           storage.UpdaterFunc,
			WatcherFunc:           storage.WatcherFunc,
			CreaterFunc:           storage.CreaterFunc,
			CollectionDeleterFunc: storage.CollectionDeleterFunc,
			GracefulDeleterFunc:   storage.GracefulDeleterFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, subresourceStorages
	}
}
