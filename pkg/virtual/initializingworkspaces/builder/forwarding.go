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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

func provideFilteredReadOnlyRestStorage(ctx context.Context, clusterClient kcpdynamic.ClusterInterface, initializer tenancyv1alpha1.WorkspaceInitializer) (apiserver.RestProviderFunc, error) {
	labelSelector := map[string]string{
		tenancyv1alpha1.WorkspacePhaseLabel: string(tenancyv1alpha1.WorkspacePhaseInitializing),
	}
	key, value := initialization.InitializerToLabel(initializer)
	labelSelector[key] = value
	requirements, selectable := labels.SelectorFromSet(labelSelector).Requirements()
	if !selectable {
		return nil, fmt.Errorf("unable to create a selector from the provided labels")
	}
	return registry.ProvideReadOnlyRestStorage(ctx, clusterClient, registry.WithStaticLabelSelector(requirements))
}

func provideDelegatingRestStorage(ctx context.Context, clusterClient kcpdynamic.ClusterInterface, initializer tenancyv1alpha1.WorkspaceInitializer) (apiserver.RestProviderFunc, error) {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		var scaleSpec *apiextensions.CustomResourceSubresourceScale

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
			"", // ClusterWorkspaces have no identity
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			clusterClient,
			nil,
			withUpdateValidation(initializer),
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

		// only expose GET
		return &struct {
			registry.FactoryFunc
			registry.ListFactoryFunc
			registry.DestroyerFunc

			registry.GetterFunc

			registry.TableConvertorFunc
			registry.CategoriesProviderFunc
			registry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc: storage.GetterFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, subresourceStorages
	}, nil
}

// withUpdateValidation adds further validation to ensure that a user of this virtual workspace can only
// remove their own initializer from the list
func withUpdateValidation(initializer tenancyv1alpha1.WorkspaceInitializer) registry.StorageWrapper {
	return func(resource schema.GroupResource, storage *registry.StoreFuncs) *registry.StoreFuncs {
		delegateUpdater := storage.UpdaterFunc
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
			validation := rest.ValidateObjectUpdateFunc(func(ctx context.Context, obj, old runtime.Object) error {
				logger := klog.FromContext(ctx)
				previous, _, err := unstructured.NestedStringSlice(old.(*unstructured.Unstructured).UnstructuredContent(), "status", "initializers")
				if err != nil {
					return errors.NewInternalError(fmt.Errorf("error accessing initializers from old object: %w", err))
				}
				current, _, err := unstructured.NestedStringSlice(obj.(*unstructured.Unstructured).UnstructuredContent(), "status", "initializers")
				if err != nil {
					logger.Error(err, "error accessing initializers from new object")
					return errors.NewInternalError(fmt.Errorf("error accessing initializers from old object: %w", err))
				}
				invalidUpdateErr := errors.NewInvalid(
					tenancyv1alpha1.Kind("ClusterWorkspace"),
					name,
					field.ErrorList{field.Invalid(
						field.NewPath("status", "initializers"),
						current,
						fmt.Sprintf("only removing the %q initializer is supported", initializer),
					)},
				)
				if len(previous)-len(current) != 1 {
					return invalidUpdateErr
				}
				for _, item := range current {
					if item == string(initializer) {
						return invalidUpdateErr
					}
				}
				return updateValidation(ctx, obj, old)
			})
			return delegateUpdater.Update(ctx, name, objInfo, createValidation, validation, forceAllowCreate, options)
		}

		return storage
	}
}
