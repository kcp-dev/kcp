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

	"github.com/google/go-cmp/cmp"

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
	finalizer corev1alpha1.LogicalClusterFinalizer,
) (apiserver.RestProviderFunc, error) {
	labelRequirement, err := finalizerLabelSetRequirement(finalizer)
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

// filteredLogicalClusterReadWriteRestStorage creates a RestProvider which will
// return LogicalClusters marked for deletion. Updates can only be made against
// the supplied finalizer.
func filteredLogicalClusterReadWriteRestStorage(
	ctx context.Context,
	clusterclient kcpdynamic.ClusterInterface,
	finalizer corev1alpha1.LogicalClusterFinalizer,
) (apiserver.RestProviderFunc, error) {
	labelRequirement, err := finalizerLabelSetRequirement(finalizer)
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
		statusSchemaValidate := subresourcesSchemaValidator["status"]

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			path.ValidatePathSegmentName,
			schemaValidator,
			statusSchemaValidate,
			structuralSchema,
			&apiextensions.CustomResourceSubresourceStatus{},
			nil, // no scale subresource needed
			[]apiextensionsv1.SelectableField{},
		)

		storage, _ := registry.NewStorage(
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
				withUpdateValidation(finalizer),
			},
		)

		// we need to expose patch and update endpoints. The Update validation will ensure that only finalizers
		// can be updated
		return &struct {
			registry.FactoryFunc
			registry.ListFactoryFunc
			registry.DestroyerFunc

			registry.GetterFunc
			registry.UpdaterFunc

			registry.TableConvertorFunc
			registry.CategoriesProviderFunc
			registry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:  storage.GetterFunc,
			UpdaterFunc: storage.UpdaterFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, nil
	}, nil
}

// withUpdateValidation wraps validateFinalizerUpdate.
func withUpdateValidation(finalizer corev1alpha1.LogicalClusterFinalizer) registry.StorageWrapper {
	return registry.StorageWrapperFunc(func(groupResource schema.GroupResource, storage *registry.StoreFuncs) {
		delegateUpdater := storage.UpdaterFunc
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *v1.UpdateOptions) (runtime.Object, bool, error) {
			validationFunc := validateFinalizerUpdate(finalizer)
			return delegateUpdater.Update(ctx, name, objInfo, createValidation, validationFunc, forceAllowCreate, options)
		}
	})
}

// validateFinalizerUpdate validates that an update to a LogicalCluster:
// * removes the passed finalizer and only that finalizer.
// * does not update any fields outside of finalizers.
func validateFinalizerUpdate(finalizer corev1alpha1.LogicalClusterFinalizer) rest.ValidateObjectUpdateFunc {
	return func(ctx context.Context, obj, old runtime.Object) error {
		logger := klog.FromContext(ctx)
		previous, err := logicalClusterFromObject(old)
		if err != nil {
			gvk := old.GetObjectKind().GroupVersionKind()
			logger.Error(nil, "cannot convert, only LogicalClusters are supported", gvk)
			return errors.NewInternalError(fmt.Errorf("cannot process %q, only LogicalClusters are supported", gvk))
		}
		current, err := logicalClusterFromObject(obj)
		if err != nil {
			gvk := obj.GetObjectKind().GroupVersionKind()
			logger.Error(nil, "cannot convert, only LogicalClusters are supported", gvk)
			return errors.NewInternalError(fmt.Errorf("cannot process %q, only LogicalClusters are supported", gvk))
		}

		// Firstly check if only the owned finalizer is being changed since it is
		// the more inexpensive check. Checking this first also avoids making a
		// potentially unnecessary copy of old and new object, to nil their allowed
		// fields
		invalidUpdateErr := errors.NewInvalid(
			corev1alpha1.Kind("LogicalCluster"),
			previous.ObjectMeta.Name,
			field.ErrorList{field.Invalid(
				field.NewPath("metadata", "finalizers"),
				current,
				fmt.Sprintf("only removing the %q finalizer from metadata.finalizers is supported", finalizer),
			)},
		)

		if len(previous.ObjectMeta.Finalizers)-len(current.ObjectMeta.Finalizers) != 1 {
			return invalidUpdateErr
		}
		if slices.Contains(current.ObjectMeta.Finalizers, string(finalizer)) {
			return invalidUpdateErr
		}

		// Secondly check if other fields than the finalizer have changed, which is
		// not allowed

		// set the allowed finalizer field to the same, so we can compare if other
		// fields have changed
		previous.ObjectMeta.Finalizers = nil
		current.ObjectMeta.Finalizers = nil

		// in addition set fields to nil which are generated
		previous.ObjectMeta.ManagedFields = nil
		current.ObjectMeta.ManagedFields = nil

		if diff := cmp.Diff(previous, current); diff != "" {
			logger.Error(nil, "only finalizers are allowed to be changed, but got", diff)
			return errors.NewInvalid(
				corev1alpha1.Kind("LogicalCluster"),
				previous.ObjectMeta.Name,
				field.ErrorList{field.Invalid(
					field.NewPath("metadata", "finalizers"),
					current,
					fmt.Sprintf("only finalizers are allowed to be changed, but got\n%s", diff),
				)},
			)
		}

		return nil
	}
}

func logicalClusterFromObject(obj runtime.Object) (*corev1alpha1.LogicalCluster, error) {
	lc := &corev1alpha1.LogicalCluster{}
	uns, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("could not cast to unstructured")
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(uns.Object, lc); err != nil {
		return nil, err
	}
	return lc, nil
}

// finalizerLabelSetRequirement creates a label requirement which requires the
// finalizer hashlabel to be set.
func finalizerLabelSetRequirement(finalizer corev1alpha1.LogicalClusterFinalizer) (labels.Requirements, error) {
	labelSelector := map[string]string{}

	key, value := termination.FinalizerToLabel(finalizer)
	labelSelector[key] = value

	requirements, selectable := labels.SelectorFromSet(labelSelector).Requirements()
	if !selectable {
		return nil, fmt.Errorf("unable to create a selector from the provided labels")
	}

	return requirements, nil
}
