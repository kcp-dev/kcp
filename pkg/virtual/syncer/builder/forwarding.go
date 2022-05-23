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
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/kube-openapi/pkg/validation/validate"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

func provideForwardingRestStorage(ctx context.Context, clusterClient dynamic.ClusterInterface, workloadClusterName, apiExportIdentityHash string) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
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
			clusterClient,
			nil,
			wrapStorageWithLabelSelector(map[string]string{workloadv1alpha1.InternalClusterResourceStateLabelPrefix + workloadClusterName: string(workloadv1alpha1.ResourceStateSync)}),
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

			registry.TableConvertorFunc
			registry.CategoriesProviderFunc
			registry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:  storage.GetterFunc,
			ListerFunc:  storage.ListerFunc,
			UpdaterFunc: storage.UpdaterFunc,
			WatcherFunc: storage.WatcherFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, subresourceStorages
	}
}

func wrapStorageWithLabelSelector(labelSelector map[string]string) registry.StorageWrapper {
	return func(resource schema.GroupResource, storage *registry.StoreFuncs) *registry.StoreFuncs {
		requirements, selectable := labels.SelectorFromSet(labels.Set(labelSelector)).Requirements()
		if !selectable {
			// we can't return an error here since this ends up inside of the k8s apiserver code where
			// no errors are expected, so the best we can do is panic - this is likely ok since there's
			// no real way that the syncer virtual workspace would ever create an unselectable selector
			panic(fmt.Sprintf("creating a new store with an unselectable set: %v", labelSelector))
		}

		delegateLister := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			selector := options.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			options.LabelSelector = selector.Add(requirements...)
			return delegateLister.List(ctx, options)
		}

		delegateGetter := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			obj, err := delegateGetter.Get(ctx, name, options)
			if err != nil {
				return obj, err
			}

			metaObj, ok := obj.(metav1.Object)
			if !ok {
				return nil, fmt.Errorf("expected a metav1.Object, got %T", obj)
			}
			if !labels.Everything().Add(requirements...).Matches(labels.Set(metaObj.GetLabels())) {
				return nil, kerrors.NewNotFound(resource, name)
			}

			return obj, err
		}

		delegateWatcher := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			selector := options.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			options.LabelSelector = selector.Add(requirements...)
			return delegateWatcher.Watch(ctx, options)
		}

		return storage
	}
}
