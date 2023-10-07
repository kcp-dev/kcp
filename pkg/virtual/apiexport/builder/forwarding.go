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

package builder

import (
	"context"
	"fmt"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/api/validation/path"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1/permissionclaims"
	"github.com/kcp-dev/logicalcluster/v3"
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

		var scaleSpec *apiextensions.CustomResourceSubresourceScale
		// TODO(sttts): implement scale subresource

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			path.ValidatePathSegmentName,
			schemaValidator,
			statusSchemaValidate,
			map[string]*structuralschema.Structural{resource.Version: structuralSchema},
			statusSpec,
			scaleSpec,
		)

		storage, statusStorage := registry.NewStorage(
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

		// TODO(sttts): add scale subresource

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
