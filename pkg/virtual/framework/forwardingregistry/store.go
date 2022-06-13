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

	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	"github.com/kcp-dev/logicalcluster"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
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

func DefaultDynamicDelegatedStoreFuncs(
	factory FactoryFunc,
	listFactory ListFactoryFunc,
	destroyerFunc DestroyerFunc,
	createStrategy rest.RESTCreateStrategy,
	updateStrategy rest.RESTUpdateStrategy,
	deleteStrategy rest.RESTDeleteStrategy,
	tableConvertor rest.TableConvertor,
	resetFieldsStrategy rest.ResetFieldsStrategy,
	resource schema.GroupVersionResource,
	apiExportIdentityHash string,
	categories []string,
	dynamicClusterClient dynamic.ClusterInterface,
	subResources []string,
	patchConflictRetryBackoff wait.Backoff,
	transformers transforming.Transformers,
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	client := clientGetter(dynamicClusterClient, createStrategy.NamespaceScoped(), resource, apiExportIdentityHash, transformers)
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
	s.CreaterFunc = nil           // not currently supported
	s.GracefulDeleterFunc = nil   // not currently supported
	s.CollectionDeleterFunc = nil // not currently supported
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
	s.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
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
				return nil, fmt.Errorf("not an Unstructured: %#v", obj)
			}

			updateStrategy.PrepareForUpdate(ctx, obj, oldObj)
			if errs := updateStrategy.ValidateUpdate(ctx, obj, oldObj); len(errs) > 0 {
				return nil, kerrors.NewInvalid(unstructuredObj.GroupVersionKind().GroupKind(), unstructuredObj.GetName(), errs)
			}
			if err := updateValidation(ctx, obj.DeepCopyObject(), oldObj.DeepCopyObject()); err != nil {
				return nil, err
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
	s.ResetFieldsStrategyFunc = resetFieldsStrategy.GetResetFields
	return s
}

func clientGetter(dynamicClusterClient dynamic.ClusterInterface, namespaceScoped bool, resource schema.GroupVersionResource, apiExportIdentityHash string, transformers transforming.Transformers) func(ctx context.Context) (dynamic.ResourceInterface, error) {
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
		client := dynamicClusterClient.Cluster(clusterName)

		if !namespaceScoped {
			return &transforming.TransformingClient{
				Transformations: transformers,
				Client:          client.Resource(gvr),
				Namespace:       "",
				Resource:        gvr,
			}, nil
		}

		if namespace, exists := genericapirequest.NamespaceFrom(ctx); exists {
			return &transforming.TransformingClient{
				Transformations: transformers,
				Client:          client.Resource(gvr).Namespace(namespace),
				Namespace:       namespace,
				Resource:        gvr,
			}, nil
		} else {
			return nil, fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String())
		}
	}
}
