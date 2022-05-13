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
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
func NewStorage(resource schema.GroupVersionResource, kind, listKind schema.GroupVersionKind, strategy customresource.CustomResourceStrategy, categories []string, tableConvertor rest.TableConvertor, replicasPathMapping fieldmanager.ResourcePathMappings,
	dynamicClusterClient dynamic.ClusterInterface, patchConflictRetryBackoff *wait.Backoff) customresource.CustomResourceStorage {
	return customresource.NewStorageWithCustomStore(resource.GroupResource(), kind, listKind, strategy, nil, categories, tableConvertor, replicasPathMapping, newStores(resource, dynamicClusterClient, patchConflictRetryBackoff))
}

func newStores(gvr schema.GroupVersionResource, dynamicClusterClient dynamic.ClusterInterface, patchConflictRetryBackoff *wait.Backoff) customresource.NewStores {
	return func(resource schema.GroupResource, kind, listKind schema.GroupVersionKind, strategy customresource.CustomResourceStrategy, optsGetter generic.RESTOptionsGetter, tableConvertor rest.TableConvertor) (main, status customresource.Store) {
		if patchConflictRetryBackoff == nil {
			patchConflictRetryBackoff = &retry.DefaultRetry
		}

		store := &Store{
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
		}

		statusStore := *store // shallow copy
		statusStrategy := customresource.NewStatusStrategy(strategy)
		statusStore.UpdateStrategy = statusStrategy
		statusStore.ResetFieldsStrategy = statusStrategy
		statusStore.subResources = []string{"status"}
		return store, &statusStore
	}
}
