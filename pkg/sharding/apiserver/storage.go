/*
Copyright 2021 The KCP Authors.

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

package apiserver

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	clientrest "k8s.io/client-go/rest"
)

type storageBase struct {
	rest.TableConvertor

	// TODO: resolve this information at runtime using an index
	shards                         map[string]*clientrest.Config
	shardIdentifierResourceVersion int64

	// we know we will have to delegate to the shards we know about, but the rest.StandardStorage
	// interface is usually used in a 1:1 mapping to underlying resources, whereas here we want
	// one of these to handle all request URIs - so, we need to inject information from our callers
	// to let us make use of the information that would otherwise be lost
	clientFor  func(cfg *clientrest.Config) (*clientrest.RESTClient, error)
	requestFor func(client *clientrest.RESTClient, mutators ...func(runtime.Object) error) (*clientrest.Request, error)
}

func (s *storageBase) New() runtime.Object {
	return &unstructured.Unstructured{}
}

func (s *storageBase) NewList() runtime.Object {
	return &unstructured.UnstructuredList{}
}

func NewMux(base storageBase) *storageMux {
	return &storageMux{
		storageBase: base,
		sharded:     &shardedStorage{storageBase: base},
		delegating:  &delegatingStorage{storageBase: base},
	}
}

type storageMux struct {
	storageBase
	sharded    listWatcher
	delegating rest.StandardStorage
}

func (s *storageMux) Destroy() {
	// Do nothing
}

func (s *storageMux) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return nil, fmt.Errorf("method not supported in a cross-cluster context")
	} else {
		return s.delegating.Get(ctx, name, options)
	}
}

func (s *storageMux) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return nil, fmt.Errorf("method not supported in a cross-cluster context")
	} else {
		return s.delegating.Create(ctx, obj, createValidation, options)
	}
}

func (s *storageMux) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return nil, false, fmt.Errorf("method not supported in a cross-cluster context")
	} else {
		return s.delegating.Update(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
	}
}

func (s *storageMux) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return nil, false, fmt.Errorf("method not supported in a cross-cluster context")
	} else {
		return s.delegating.Delete(ctx, name, deleteValidation, options)
	}
}

func (s *storageMux) DeleteCollection(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *internalversion.ListOptions) (runtime.Object, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return nil, fmt.Errorf("method not supported in a cross-cluster context")
	} else {
		return s.delegating.DeleteCollection(ctx, deleteValidation, options, listOptions)
	}
}

func (s *storageMux) List(ctx context.Context, options *internalversion.ListOptions) (runtime.Object, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return s.sharded.List(ctx, options)
	} else {
		return s.delegating.List(ctx, options)
	}
}

func (s *storageMux) Watch(ctx context.Context, options *internalversion.ListOptions) (watch.Interface, error) {
	if c := request.ClusterFrom(ctx); c.Wildcard {
		return s.sharded.Watch(ctx, options)
	} else {
		return s.delegating.Watch(ctx, options)
	}
}

var _ rest.StandardStorage = &storageMux{}
