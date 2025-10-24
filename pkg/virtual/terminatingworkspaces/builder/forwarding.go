/*
Copyright 2025 The KCP Authors.

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
	"slices"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation/path"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/termination"
)

// filteredLogicalClusterReadWriteRestStorage creates a RestProvider which will
// return LogicalClusters marked for deletion.
func filteredLogicalClusterReadOnlyRestStorage(
	ctx context.Context,
	clusterClient kcpdynamic.ClusterInterface,
	terminator corev1alpha1.LogicalClusterTerminator,
) (apiserver.RestProviderFunc, error) {
	labelRequirement, err := terminatorLabelSetRequirement(terminator)
	if err != nil {
		return nil, err
	}

	return registry.ProvideReadOnlyRestStorage(
		ctx,
		func(ctx context.Context) (kcpdynamic.ClusterInterface, error) { return clusterClient, nil },
		&registry.StorageWrappers{
			registry.WithDeletionTimestamp(),
			registry.WithStaticLabelSelector(labelRequirement),
		},
		nil,
	)
}

// filteredLogicalClusterStatusWriteOnly creates a RestProvider which will
// return LogicalClusters marked for deletion. Updates can only be made against
// the supplied terminator.
func filteredLogicalClusterStatusWriteOnly(
	ctx context.Context,
	clusterclient kcpdynamic.ClusterInterface,
	terminator corev1alpha1.LogicalClusterTerminator,
) (apiserver.RestProviderFunc, error) {
	labelRequirement, err := terminatorLabelSetRequirement(terminator)
	if err != nil {
		return nil, err
	}

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
		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			path.ValidatePathSegmentName,
			schemaValidator,
			statusSchemaValidate,
			structuralSchema,
			statusSpec,
			nil, // no scale subresource needed
			[]apiextensionsv1.SelectableField{},
		)

		storage, statusStorage := registry.NewStorage(
			ctx,
			resource,
			"", // no hash, as this is not backed by an APIExport
			kind,
			listKind,
			strategy,
			nil, // currently we are not using any categories
			tableConvertor,
			nil, // we are not using any replicasPathMapping
			func(ctx context.Context) (kcpdynamic.ClusterInterface, error) { return clusterclient, nil },
			nil, // use the default retryBackoff
			&registry.StorageWrappers{
				registry.WithDeletionTimestamp(),
				registry.WithStaticLabelSelector(labelRequirement),
				withUpdateValidation(terminator),
			},
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

		// only expose GET on the regular storage
		storages := &struct {
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
		}
		return storages, subresourceStorages
	}, nil
}

// withUpdateValidation wraps validateTerminatorStatusUpdate.
func withUpdateValidation(terminator corev1alpha1.LogicalClusterTerminator) registry.StorageWrapper {
	return registry.StorageWrapperFunc(func(groupResource schema.GroupResource, storage *registry.StoreFuncs) {
		delegateUpdater := storage.UpdaterFunc
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *v1.UpdateOptions) (runtime.Object, bool, error) {
			// we only need to validate the status sub-resource, as any other types of updates are not possible on a storage layer
			validationFunc := validateTerminatorStatusUpdate(terminator, name)
			return delegateUpdater.Update(ctx, name, objInfo, createValidation, validationFunc, forceAllowCreate, options)
		}
	})
}

// validateTerminatorStatusUpdate validates that an update to a LogicalCluster only removes the passed terminator and only that terminator.
func validateTerminatorStatusUpdate(terminator corev1alpha1.LogicalClusterTerminator, name string) rest.ValidateObjectUpdateFunc {
	return func(ctx context.Context, obj, old runtime.Object) error {
		logger := klog.FromContext(ctx)
		previous, _, err := unstructured.NestedStringSlice(old.(*unstructured.Unstructured).UnstructuredContent(), "status", "terminators")
		if err != nil {
			logger.Error(err, "error accessing terminators from old object")
			return errors.NewInternalError(fmt.Errorf("error accessing terminators from old object: %w", err))
		}
		current, _, err := unstructured.NestedStringSlice(obj.(*unstructured.Unstructured).UnstructuredContent(), "status", "terminators")
		if err != nil {
			logger.Error(err, "error accessing terminators from new object")
			return errors.NewInternalError(fmt.Errorf("error accessing terminators from old object: %w", err))
		}

		invalidUpdateErr := errors.NewInvalid(
			corev1alpha1.Kind("LogicalCluster"),
			name,
			field.ErrorList{field.Invalid(
				field.NewPath("status", "terminators"),
				current,
				fmt.Sprintf("only removing the %q terminator is supported", terminator),
			)},
		)

		if len(previous)-len(current) != 1 {
			return invalidUpdateErr
		}
		if slices.Contains(current, string(terminator)) {
			return invalidUpdateErr
		}

		return nil
	}
}

// terminatorLabelSetRequirement creates a label requirement which requires the
// terminator hashlabel to be set.
func terminatorLabelSetRequirement(terminator corev1alpha1.LogicalClusterTerminator) (labels.Requirements, error) {
	labelSelector := map[string]string{}

	key, value := termination.TerminatorToLabel(terminator)
	labelSelector[key] = value

	requirements, selectable := labels.SelectorFromSet(labelSelector).Requirements()
	if !selectable {
		return nil, fmt.Errorf("unable to create a selector from the provided labels")
	}

	return requirements, nil
}
