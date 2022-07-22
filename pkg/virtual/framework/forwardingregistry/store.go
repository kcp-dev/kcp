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

	"github.com/kcp-dev/logicalcluster/v2"

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
	dynamicClusterClient dynamic.ClusterInterface,
	subResources []string,
	patchConflictRetryBackoff wait.Backoff,
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	client := clientGetter(dynamicClusterClient, strategy.NamespaceScoped(), resource, apiExportIdentityHash)
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

		deleter, err := withDeleter(delegate)
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

		deleter, err := withDeleter(delegate)
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

		delegate, err := client(ctx)
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

		doUpdate := func() (*unstructured.Unstructured, error) {
			oldObj, err := s.Get(ctx, name, &metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			obj, err := objInfo.UpdatedObject(ctx, oldObj)
			if err != nil {
				return nil, err
			}

			unstructuredObj, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("not an Unstructured: %T", obj)
			}

			return delegate.Update(ctx, unstructuredObj, *options, subResources...)
		}

		requestInfo, _ := genericapirequest.RequestInfoFrom(ctx)
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
		delegate, err := client(ctx)
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

func withDeleter(dynamicResourceInterface dynamic.ResourceInterface) (dynamicextension.ResourceInterface, error) {
	if c, ok := dynamicResourceInterface.(dynamicextension.ResourceInterface); ok {
		return c, nil
	}
	return nil, fmt.Errorf("dynamic client does not implement ResourceDeleterInterface")
}

func clientGetter(dynamicClusterClient dynamic.ClusterInterface, namespaceScoped bool, resource schema.GroupVersionResource, apiExportIdentityHash string) func(ctx context.Context) (dynamic.ResourceInterface, error) {
	return func(ctx context.Context) (dynamic.ResourceInterface, error) {
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return nil, err
		}
		gvr := resource
		clusterName := cluster.Name
		if cluster.Wildcard {
			clusterName = logicalcluster.Wildcard
			if apiExportIdentityHash != "" {
				gvr.Resource += ":" + apiExportIdentityHash
			}
		}

		if namespaceScoped {
			if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
				return dynamicClusterClient.Cluster(clusterName).Resource(gvr).Namespace(namespace), nil
			} else {
				return nil, fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String())
			}
		} else {
			return dynamicClusterClient.Cluster(clusterName).Resource(gvr), nil
		}
	}
}
