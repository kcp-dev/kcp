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

	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/handlers/fieldmanager"
	"k8s.io/apiserver/pkg/registry/generic"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

// NewStorage returns a REST storage that forwards calls to a dynamic client
func NewStorage(ctx context.Context, resource schema.GroupVersionResource, kind, listKind schema.GroupVersionKind, strategy customresource.CustomResourceStrategy, categories []string, tableConvertor rest.TableConvertor, replicasPathMapping fieldmanager.ResourcePathMappings,
	dynamicClusterClient dynamic.ClusterInterface, patchConflictRetryBackoff *wait.Backoff, labelSelector map[string]string) customresource.CustomResourceStorage {
	stores := newStores(ctx, resource, dynamicClusterClient, patchConflictRetryBackoff, labelSelector)
	return customresource.NewStorageWithCustomStore(resource.GroupResource(), kind, listKind, strategy, nil, categories, tableConvertor, replicasPathMapping, stores)
}

func newStores(ctx context.Context, gvr schema.GroupVersionResource, dynamicClusterClient dynamic.ClusterInterface, patchConflictRetryBackoff *wait.Backoff, labelSelector map[string]string) customresource.NewStores {
	return func(resource schema.GroupResource, kind, listKind schema.GroupVersionKind, strategy customresource.CustomResourceStrategy, optsGetter generic.RESTOptionsGetter, tableConvertor rest.TableConvertor) (main, status customresource.Store) {
		if patchConflictRetryBackoff == nil {
			patchConflictRetryBackoff = &retry.DefaultRetry
		}

		requirements, selectable := labels.SelectorFromSet(labels.Set(labelSelector)).Requirements()
		if !selectable {
			panic(fmt.Sprintf("creating a new store with an unselectable set: %v", labelSelector))
		}

		delegate := &Store{
			NewFunc: func() runtime.Object {
				// set the expected group/version/kind in the new object as a signal to the versioning decoder
				ret := &unstructured.Unstructured{}
				ret.SetGroupVersionKind(kind)
				return ret
			},
			NewListFunc: func() runtime.Object {
				// lists are never stored, only manufactured, so stomp in the right kind
				ret := &unstructured.UnstructuredList{}
				ret.SetGroupVersionKind(listKind)
				return ret
			},
			DefaultQualifiedResource: resource,
			CreateStrategy:           strategy,
			UpdateStrategy:           strategy,
			DeleteStrategy:           strategy,
			TableConvertor:           tableConvertor,
			ResetFieldsStrategy:      strategy,

			resource:                  gvr,
			dynamicClusterClient:      dynamicClusterClient,
			patchConflictRetryBackoff: *patchConflictRetryBackoff,
			stopWatchesCh:             ctx.Done(),
		}
		store := &LabelSelectingStore{
			Store:  delegate,
			filter: requirements,
		}
		delegate.getter = store

		statusDelegate := *delegate // shallow copy
		statusStrategy := customresource.NewStatusStrategy(strategy)
		statusDelegate.UpdateStrategy = statusStrategy
		statusDelegate.ResetFieldsStrategy = statusStrategy
		statusDelegate.subResources = []string{"status"}
		statusStore := &LabelSelectingStore{
			Store:  &statusDelegate,
			filter: requirements,
		}
		statusDelegate.getter = &statusDelegate
		return store, statusStore
	}
}
