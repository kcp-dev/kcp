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
	"fmt"
	"net/http"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	dynamicextension "github.com/kcp-dev/kcp/pkg/virtual/framework/client/dynamic"
)

// StoreFuncs holds proto-functions that can be mutated by successive actors to wrap behavior.
// Ultimately you can pick and choose which functions to expose in the end, depending on how much
// of REST storage you need.
type StoreFuncs struct {
	FactoryFunc
	ListFactoryFunc
	DestroyerFunc

	GetterFunc
	CreaterFunc
	GracefulDeleterFunc
	CollectionDeleterFunc
	ListerFunc
	UpdaterFunc
	WatcherFunc

	TableConvertorFunc
	CategoriesProviderFunc
	ResetFieldsStrategyFunc
}

type Strategy interface {
	rest.RESTCreateStrategy
	rest.ResetFieldsStrategy
}

func DefaultDynamicDelegatedStoreFuncs(
	factory FactoryFunc,
	listFactory ListFactoryFunc,
	destroyerFunc DestroyerFunc,
	strategy Strategy,
	tableConvertor rest.TableConvertor,
	resource schema.GroupVersionResource,
	apiExportIdentityHash string,
	categories []string,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	subResources []string,
	patchConflictRetryBackoff wait.Backoff,
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	client := clientGetter(dynamicClusterClient, strategy.NamespaceScoped(), resource, apiExportIdentityHash)
	listerWatcher := listerWatcherGetter(dynamicClusterClient, strategy.NamespaceScoped(), resource, apiExportIdentityHash)
	s := &StoreFuncs{}
	s.FactoryFunc = factory
	s.ListFactoryFunc = listFactory
	s.DestroyerFunc = destroyerFunc
	s.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		return delegate.Get(ctx, name, *options, subResources...)
	}
	s.CreaterFunc = func(ctx context.Context, obj runtime.Object, _ rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
		unstructuredObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("not an Unstructured: %T", obj)
		}

		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		return delegate.Create(ctx, unstructuredObj, *options, subResources...)
	}
	s.GracefulDeleterFunc = func(ctx context.Context, name string, _ rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, false, err
		}

		deleter, err := dynamicextension.NewDeleterWithResults(delegate)
		if err != nil {
			return nil, false, err
		}

		obj, status, err := deleter.DeleteWithResult(ctx, name, *options, subResources...)
		if err != nil {
			return nil, false, err
		}

		deletedImmediately := true
		if status == http.StatusAccepted {
			deletedImmediately = false
		}

		if obj.GetObjectKind().GroupVersionKind() == metav1.Unversioned.WithKind("Status") {
			// The DELETE request made to the downstream API server can either return the full object,
			// or a Status object, depending on some options and immediate deletion.
			// In the later case, the default encoder does not have the Status kind GVK registered,
			// and fails to serialize it to the response, when it's provided as an unstructured,
			// so we do the conversion upfront.
			status := &metav1.Status{}
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), status)
			return status, deletedImmediately, err
		}

		return obj, deletedImmediately, nil
	}
	s.CollectionDeleterFunc = func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		deleter, err := dynamicextension.NewDeleterWithResults(delegate)
		if err != nil {
			return nil, err
		}

		var v1ListOptions metav1.ListOptions
		err = metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(listOptions, &v1ListOptions, nil)
		if err != nil {
			return nil, err
		}

		return deleter.DeleteCollectionWithResult(ctx, *options, v1ListOptions)
	}
	s.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
		var v1ListOptions metav1.ListOptions
		if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
			return nil, err
		}

		delegate, err := listerWatcher(ctx)
		if err != nil {
			return nil, err
		}

		return delegate.List(ctx, v1ListOptions)
	}
	s.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, _ rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, false, err
		}

		requestInfo, _ := genericapirequest.RequestInfoFrom(ctx)

		doUpdate := func() (*unstructured.Unstructured, error) {
			oldObj, err := s.Get(ctx, name, &metav1.GetOptions{})
			if err != nil {
				// Continue on 404 when forceAllowCreate is enabled,
				// or for PATCH requests, so server-side apply requests
				// for non-existent objects can still be processed.
				if !apierrors.IsNotFound(err) ||
					!forceAllowCreate &&
						!(requestInfo != nil && requestInfo.Verb == "patch") {
					return nil, err
				}
				oldObj = nil
			}

			// The following call returns a 404 error for non server-side apply
			// requests, i.e., for json, merge and strategic-merge PATCH requests,
			// as it's not possible to construct the updated object out of the patch
			// alone, when the object does not already exist.
			// For server-side apply, the computed object is used as the body of the
			// PUT request below, to create the object from the apply patch.
			obj, err := objInfo.UpdatedObject(ctx, oldObj)
			if err != nil {
				return nil, err
			}

			unstructuredObj, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("not an Unstructured: %T", obj)
			}

			if oldObj == nil {
				// The object does not currently exist.
				// We switch to calling a create operation on the forwarding registry.
				// This enables support for server-side apply requests, to create non-existent objects.
				return delegate.Create(ctx, unstructuredObj, updateToCreateOptions(options), subResources...)
			}
			return delegate.Update(ctx, unstructuredObj, *options, subResources...)
		}

		if requestInfo != nil && requestInfo.Verb == "patch" {
			var result *unstructured.Unstructured
			err := retry.RetryOnConflict(patchConflictRetryBackoff, func() error {
				var err error
				result, err = doUpdate()
				return err
			})
			return result, false, err
		}

		result, err := doUpdate()
		return result, false, err
	}

	s.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
		var v1ListOptions metav1.ListOptions
		if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
			return nil, err
		}
		delegate, err := listerWatcher(ctx)
		if err != nil {
			return nil, err
		}

		watchCtx, cancelFn := context.WithCancel(ctx)
		go func() {
			select {
			case <-stopWatchesCh:
				cancelFn()
			case <-ctx.Done():
				return
			}
		}()

		return delegate.Watch(watchCtx, v1ListOptions)
	}
	s.TableConvertorFunc = tableConvertor.ConvertToTable
	s.CategoriesProviderFunc = func() []string {
		return categories
	}
	s.ResetFieldsStrategyFunc = strategy.GetResetFields
	return s
}

func clientGetter(dynamicClusterClient kcpdynamic.ClusterInterface, namespaceScoped bool, resource schema.GroupVersionResource, apiExportIdentityHash string) func(ctx context.Context) (dynamic.ResourceInterface, error) {
	return func(ctx context.Context) (dynamic.ResourceInterface, error) {
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return nil, apiErrorBadRequest(err)
		}
		gvr := resource
		clusterName := cluster.Name
		if apiExportIdentityHash != "" {
			gvr.Resource += ":" + apiExportIdentityHash
		}

		if namespaceScoped {
			if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
				return dynamicClusterClient.Cluster(clusterName.Path()).Resource(gvr).Namespace(namespace), nil
			} else {
				return nil, apiErrorBadRequest(fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String()))
			}
		} else {
			return dynamicClusterClient.Cluster(clusterName.Path()).Resource(gvr), nil
		}
	}
}

type listerWatcher interface {
	List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

func listerWatcherGetter(dynamicClusterClient kcpdynamic.ClusterInterface, namespaceScoped bool, resource schema.GroupVersionResource, apiExportIdentityHash string) func(ctx context.Context) (listerWatcher, error) {
	return func(ctx context.Context) (listerWatcher, error) {
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return nil, apiErrorBadRequest(err)
		}
		gvr := resource
		if apiExportIdentityHash != "" {
			gvr.Resource += ":" + apiExportIdentityHash
		}
		namespace, namespaceSet := genericapirequest.NamespaceFrom(ctx)

		switch {
		case cluster.Wildcard:
			if namespaceScoped && namespaceSet && namespace != metav1.NamespaceAll {
				return nil, apiErrorBadRequest(fmt.Errorf("cross-cluster LIST and WATCH are required to be cross-namespace, not scoped to namespace %s", namespace))
			}
			return dynamicClusterClient.Resource(gvr), nil
		default:
			if namespaceScoped {
				if !namespaceSet {
					return nil, apiErrorBadRequest(fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String()))
				}
				return dynamicClusterClient.Cluster(cluster.Name.Path()).Resource(gvr).Namespace(namespace), nil
			}
			return dynamicClusterClient.Cluster(cluster.Name.Path()).Resource(gvr), nil
		}
	}
}

// updateToCreateOptions creates a CreateOptions with the same field values as the provided PatchOptions.
func updateToCreateOptions(uo *metav1.UpdateOptions) metav1.CreateOptions {
	co := metav1.CreateOptions{
		DryRun:          uo.DryRun,
		FieldManager:    uo.FieldManager,
		FieldValidation: uo.FieldValidation,
	}
	co.TypeMeta.SetGroupVersionKind(metav1.SchemeGroupVersion.WithKind("CreateOptions"))
	return co
}

// apiErrorBadRequest returns a apierrors.StatusError with a BadRequest reason.
func apiErrorBadRequest(err error) *apierrors.StatusError {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusBadRequest,
		Message: err.Error(),
	}}
}
