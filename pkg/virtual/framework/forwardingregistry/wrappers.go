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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
)

func WithLabelSelector(labelSelector map[string]string) StorageWrapper {
	return func(resource schema.GroupResource, storage *StoreFuncs) *StoreFuncs {
		requirements, selectable := labels.SelectorFromSet(labels.Set(labelSelector)).Requirements()
		if !selectable {
			// we can't return an error here since this ends up inside of the k8s apiserver code where
			// no errors are expected, so the best we can do is panic - this is likely ok since there's
			// no real way that the syncer virtual workspace would ever create an unselectable selector
			panic(fmt.Sprintf("creating a new store with an unselectable set: %v", labelSelector))
		}

		delegateLister := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
			selector := options.LabelSelector
			if selector == nil {
				selector = labels.Everything()
			}
			options.LabelSelector = selector.Add(requirements...)
			return delegateLister.List(ctx, options)
		}

		delegateGetter := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *v1.GetOptions) (runtime.Object, error) {
			obj, err := delegateGetter.Get(ctx, name, options)
			if err != nil {
				return obj, err
			}

			metaObj, ok := obj.(v1.Object)
			if !ok {
				return nil, fmt.Errorf("expected a metav1.Object, got %T", obj)
			}
			if !labels.Everything().Add(requirements...).Matches(labels.Set(metaObj.GetLabels())) {
				return nil, errors.NewNotFound(resource, name)
			}

			return obj, err
		}

		delegateWatcher := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
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
