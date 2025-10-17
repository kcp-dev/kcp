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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
)

func WithStaticLabelSelector(labelSelector labels.Requirements) StorageWrapper {
	return WithLabelSelector(func(ctx context.Context) labels.Requirements {
		return labelSelector
	})
}

func WithLabelSelector(labelSelectorFrom func(ctx context.Context) labels.Requirements) StorageWrapper {
	return StorageWrapperFunc(func(resource schema.GroupResource, storage *StoreFuncs) {
		delegateLister := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
			selector := options.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			options.LabelSelector = selector.Add(labelSelectorFrom(ctx)...)
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
			if !labels.Everything().Add(labelSelectorFrom(ctx)...).Matches(labels.Set(metaObj.GetLabels())) {
				return nil, errors.NewNotFound(resource, name)
			}

			return obj, err
		}

		delegateCollectionDeleterFunc := storage.CollectionDeleterFunc
		storage.CollectionDeleterFunc = func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *internalversion.ListOptions) (runtime.Object, error) {
			selector := listOptions.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			listOptions.LabelSelector = selector.Add(labelSelectorFrom(ctx)...)
			return delegateCollectionDeleterFunc.DeleteCollection(ctx, deleteValidation, options, listOptions)
		}

		delegateWatcher := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
			selector := options.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			options.LabelSelector = selector.Add(labelSelectorFrom(ctx)...)
			return delegateWatcher.Watch(ctx, options)
		}
	})
}

// WithDeletionTimestamp creates a StorageWrapper which only returns objects which are marked
// for deletion and have a deletion timestamp.
func WithDeletionTimestamp() StorageWrapper {
	return StorageWrapperFunc(func(groupResource schema.GroupResource, storage *StoreFuncs) {
		delegateGetter := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			obj, err := delegateGetter.Get(ctx, name, options)
			if err != nil {
				return nil, err
			}

			if !hasDeletionTimestamp(ctx, obj) {
				return nil, errors.NewNotFound(groupResource, name)
			}

			return obj, err
		}

		delegateLister := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateLister.List(ctx, options)
			if err != nil {
				return nil, err
			}

			ul, _ := result.(*unstructured.UnstructuredList)

			filtered := []unstructured.Unstructured{}
			for i, item := range ul.Items {
				if hasDeletionTimestamp(ctx, &item) {
					filtered = append(filtered, ul.Items[i])
				}
			}

			ul.Items = filtered

			return ul, nil
		}

		delegateWatcher := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
			wi, err := delegateWatcher.Watch(ctx, options)
			if err != nil {
				return nil, err
			}

			filtered := watch.Filter(wi, func(in watch.Event) (out watch.Event, keep bool) {
				return in, hasDeletionTimestamp(ctx, in.Object)
			})

			return filtered, nil
		}
	})
}

// hasDeletionTimestamp returns whether a runtime.object has a deletion timestamp
// wrapping the meta.Objects deletiontimestamp functionality.
func hasDeletionTimestamp(ctx context.Context, obj runtime.Object) bool {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		// should never happen
		return false
	}

	if !metaObj.GetDeletionTimestamp().IsZero() {
		return true
	}
	return false
}
