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

package forwardingregistry

import (
	"context"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/handlers/fieldmanager"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
)

// StorageWrapper allows consumers to wrap the delegating Store in order to add custom behavior around it.
// For example, a consumer might want to filter the results from the delegated Store, or add impersonation to it.
type StorageWrapper func(schema.GroupResource, *StoreFuncs) *StoreFuncs

// NewStorage returns a REST storage that forwards calls to a dynamic client
func NewStorage(ctx context.Context, resource schema.GroupVersionResource, apiExportIdentityHash string, kind, listKind schema.GroupVersionKind, strategy customresource.CustomResourceStrategy, categories []string, tableConvertor rest.TableConvertor, replicasPathMapping fieldmanager.ResourcePathMappings,
	dynamicClusterClient dynamic.ClusterInterface, patchConflictRetryBackoff *wait.Backoff, wrapper StorageWrapper) (mainStorage, statusStorage *StoreFuncs) {
	if patchConflictRetryBackoff == nil {
		patchConflictRetryBackoff = &retry.DefaultRetry
	}
	if wrapper == nil {
		wrapper = func(_ schema.GroupResource, funcs *StoreFuncs) *StoreFuncs { return funcs }
	}

	factory := func() runtime.Object {
		// set the expected group/version/kind in the new object as a signal to the versioning decoder
		ret := &unstructured.Unstructured{}
		ret.SetGroupVersionKind(kind)
		return ret
	}
	listFactory := func() runtime.Object {
		// lists are never stored, only manufactured, so stomp in the right kind
		ret := &unstructured.UnstructuredList{}
		ret.SetGroupVersionKind(listKind)
		return ret
	}
	destroyer := func() {
		// TODO: what do we do on Destroy()?
	}

	delegate := DefaultDynamicDelegatedStoreFuncs(
		factory, listFactory, destroyer,
		strategy, tableConvertor,
		resource, apiExportIdentityHash, categories,
		dynamicClusterClient, []string{}, *patchConflictRetryBackoff, ctx.Done(),
	)
	store := wrapper(resource.GroupResource(), delegate)

	statusStrategy := customresource.NewStatusStrategy(strategy)
	statusDelegate := DefaultDynamicDelegatedStoreFuncs(
		factory, listFactory, destroyer,
		statusStrategy, tableConvertor,
		resource, apiExportIdentityHash, categories,
		dynamicClusterClient, []string{"status"}, *patchConflictRetryBackoff, ctx.Done(),
	)
	delegateUpdate := statusDelegate.UpdaterFunc
	statusDelegate.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
		// We are explicitly setting forceAllowCreate to false in the call to the underlying storage because
		// subresources should never allow create on update.
		return delegateUpdate(ctx, name, objInfo, createValidation, updateValidation, false, options)
	}
	statusStore := wrapper(resource.GroupResource(), statusDelegate)
	return store, statusStore
}

// ProvideReadOnlyRestStorage returns a commonly used REST storage that forwards calls to a dynamic client,
// but only for read-only requests.
func ProvideReadOnlyRestStorage(ctx context.Context, clusterClient dynamic.ClusterInterface, wrapper StorageWrapper) (apiserver.RestProviderFunc, error) {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		statusSchemaValidate := subresourcesSchemaValidator["status"]

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			schemaValidator,
			statusSchemaValidate,
			map[string]*structuralschema.Structural{resource.Version: structuralSchema},
			nil, // no status here
			nil, // no scale here
		)

		storage, _ := NewStorage(
			ctx,
			resource,
			"",
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			clusterClient,
			nil,
			wrapper,
		)

		// only expose LIST+WATCH
		return &struct {
			FactoryFunc
			ListFactoryFunc
			DestroyerFunc

			GetterFunc
			ListerFunc
			WatcherFunc

			TableConvertorFunc
			CategoriesProviderFunc
			ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:  storage.GetterFunc,
			ListerFunc:  storage.ListerFunc,
			WatcherFunc: storage.WatcherFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, nil // no subresources
	}, nil
}
