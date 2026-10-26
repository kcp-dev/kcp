/*
Copyright 2026 The kcp Authors.

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
	"errors"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	"github.com/kcp-dev/kcp/pkg/apiidentity"
)

// provideAPIIdentitiesRestStorage builds the REST storage for the identity
// view: GET/LIST/WATCH computed by the projector from the APIExports
// replicated to the cache server. Nothing is stored and nothing is written:
// the view is read-only.
func provideAPIIdentitiesRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	projector *apiidentity.Projector,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	// the view is computed; the storage never reaches for a client.
	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return nil, errors.New("the identity view is computed, it has no backing client")
	})

	restProvider, err := forwardingregistry.ProvideReadOnlyRestStorage(ctx, clientFunc, projectedView(projector), nil)
	if err != nil {
		cancelFn()
		return nil, err
	}

	def, err := apiserver.CreateServingInfoFor(mainConfig, APIIdentitiesSchema, adminv1alpha1.SchemeGroupVersion.Version, restProvider)
	if err != nil {
		cancelFn()
		return nil, err
	}

	return &apiDefinitionWithCancel{
		APIDefinition: def,
		cancelFn:      cancelFn,
	}, nil
}

var apiIdentitiesResource = schema.GroupResource{Group: adminv1alpha1.SchemeGroupVersion.Group, Resource: "apiidentities"}

// projectedView replaces the reads of the storage with the projector's view.
func projectedView(projector *apiidentity.Projector) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		storage.GetterFunc = func(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
			entry, ok := projector.Get(name)
			if !ok {
				return nil, apierrors.NewNotFound(apiIdentitiesResource, name)
			}
			return toUnstructured(entry)
		}

		storage.ListerFunc = func(_ context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			list := &unstructured.UnstructuredList{}
			list.SetAPIVersion(adminv1alpha1.SchemeGroupVersion.String())
			list.SetKind("APIIdentityList")
			list.SetResourceVersion(projector.ResourceVersion())
			for _, entry := range projector.List(options.LabelSelector) {
				if !matchesFields(options.FieldSelector, entry) {
					continue
				}
				obj, err := toUnstructured(entry)
				if err != nil {
					return nil, err
				}
				list.Items = append(list.Items, *obj)
			}
			return list, nil
		}

		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			w, err := projector.Watch(options.LabelSelector, apiidentity.WatchOptions{
				ResourceVersion:     options.ResourceVersion,
				SendInitialEvents:   ptr.Deref(options.SendInitialEvents, false),
				AllowWatchBookmarks: options.AllowWatchBookmarks,
			})
			if err != nil {
				return nil, err
			}
			return newConvertingWatch(ctx, w, options.FieldSelector), nil
		}
	})
}

// matchesFields applies the one field selector the view supports,
// metadata.name.
func matchesFields(selector fields.Selector, entry *adminv1alpha1.APIIdentity) bool {
	return selector == nil || selector.Empty() || selector.Matches(fields.Set{"metadata.name": entry.Name})
}

func toUnstructured(entry *adminv1alpha1.APIIdentity) (*unstructured.Unstructured, error) {
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(entry)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: content}, nil
}

// convertingWatch forwards the projector's typed events as unstructured
// objects, which is what the storage serves, dropping those a field
// selector excludes. Bookmarks and errors pass through.
type convertingWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
	stopOnce   sync.Once
}

func newConvertingWatch(ctx context.Context, delegate watch.Interface, selector fields.Selector) *convertingWatch {
	w := &convertingWatch{
		delegate:   delegate,
		resultChan: make(chan watch.Event, 100), // Matches outgoingBufSize=100 in k8s.io/apiserver/pkg/storage/etcd3/watcher.go.
	}

	go func() {
		defer close(w.resultChan)
		for {
			select {
			case event, ok := <-delegate.ResultChan():
				if !ok {
					return
				}
				if entry, ok := event.Object.(*adminv1alpha1.APIIdentity); ok {
					if event.Type != watch.Bookmark && !matchesFields(selector, entry) {
						continue
					}
					obj, err := toUnstructured(entry)
					if err != nil {
						event = watch.Event{Type: watch.Error, Object: &apierrors.NewInternalError(err).ErrStatus}
					} else {
						event.Object = obj
					}
				}
				select {
				case w.resultChan <- event:
				case <-ctx.Done():
					delegate.Stop()
					return
				}
			case <-ctx.Done():
				delegate.Stop()
				return
			}
		}
	}()
	return w
}

func (w *convertingWatch) Stop() {
	w.stopOnce.Do(func() { w.delegate.Stop() })
}

func (w *convertingWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
