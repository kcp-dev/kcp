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
	goruntime "runtime"

	"github.com/kcp-dev/logicalcluster"
	"golang.org/x/sync/errgroup"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
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
	MayReturnFullObjectDeleterFunc
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
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	client := clientGetter(dynamicClusterClient, createStrategy.NamespaceScoped(), resource, apiExportIdentityHash)
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
		err := rest.BeforeCreate(createStrategy, ctx, obj)
		if err != nil {
			return nil, err
		}
		unstructuredObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("not an Unstructured: %#v", obj)
		}
		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}
		return delegate.Create(ctx, unstructuredObj, *options, subResources...)
	}
	s.GracefulDeleterFunc = func(ctx context.Context, name string, _ rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
		obj, err := s.Get(ctx, name, &metav1.GetOptions{})
		if err != nil {
			return nil, false, err
		}
		graceful, gracefulPending, err := rest.BeforeDelete(deleteStrategy, ctx, obj, options)
		if err != nil {
			return nil, false, err
		}
		accessor, err := meta.Accessor(obj)
		if err != nil {
			return nil, false, err
		}
		opts := options.DeepCopy()
		if opts.Preconditions == nil {
			opts.Preconditions = &metav1.Preconditions{}
		}
		if opts.Preconditions.UID == nil {
			uid := accessor.GetUID()
			opts.Preconditions.UID = &uid
		}
		if opts.Preconditions.ResourceVersion == nil {
			rv := accessor.GetResourceVersion()
			opts.Preconditions.ResourceVersion = &rv
		}
		delegate, err := client(ctx)
		if err != nil {
			return nil, false, err
		}
		err = delegate.Delete(ctx, name, *opts, subResources...)
		if err != nil {
			return nil, false, err
		}
		return obj, !graceful && !gracefulPending, nil
	}
	s.MayReturnFullObjectDeleterFunc = func() bool {
		return true
	}
	s.CollectionDeleterFunc = func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
		list, err := s.List(ctx, listOptions)
		if err != nil {
			return nil, err
		}
		count := meta.LenList(list)
		if count == 0 {
			// Nothing to delete, return now
			return list, nil
		}

		workers := goruntime.GOMAXPROCS(0)
		if workers > count {
			workers = count
		}

		objects := make(chan runtime.Object, workers)
		var deletedObjects []runtime.Object

		group, gCtx := errgroup.WithContext(ctx)

		group.Go(func() error {
			defer close(objects)
			return meta.EachListItem(list, func(object runtime.Object) error {
				objects <- object
				return nil
			})
		})

		for i := 0; i < workers; i++ {
			group.Go(func() error {
				for obj := range objects {
					accessor, err := meta.Accessor(obj)
					if err != nil {
						return err
					}
					deleted, _, err := s.Delete(gCtx, accessor.GetName(), deleteValidation, options.DeepCopy())
					if err != nil && !apierrors.IsNotFound(err) {
						return err
					}
					deletedObjects = append(deletedObjects, deleted)
				}
				return nil
			})
		}

		err = group.Wait()
		if err != nil {
			if e := meta.SetList(list, deletedObjects); e != nil {
				return nil, errors.NewAggregate([]error{err, e})
			}
			return list, err
		}

		err = meta.SetList(list, deletedObjects)
		if err != nil {
			return nil, err
		}

		return list, nil
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
			err = rest.BeforeUpdate(updateStrategy, ctx, obj, oldObj)
			if err != nil {
				return nil, err
			}
			unstructuredObj, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("not an Unstructured: %#v", obj)
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
		client := dynamicClusterClient.Cluster(clusterName)

		if namespaceScoped {
			if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
				return client.Resource(gvr).Namespace(namespace), nil
			} else {
				return nil, fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String())
			}
		} else {
			return client.Resource(gvr), nil
		}
	}
}
