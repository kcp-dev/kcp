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

package compositecmd

import (
	"context"

	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
)

var _ rest.Storage = &BasicStorage{}
var _ rest.Scoper = &BasicStorage{}
var _ rest.GroupVersionKindProvider = &BasicStorage{}
var _ rest.Lister = &BasicStorage{}
var _ rest.Creater = &Creater{}
var _ rest.GracefulDeleter = &Deleter{}
var _ rest.Getter = &BasicStorage{}
var _ rest.Watcher = &Watcher{}

type BasicStorage struct {
	GVK               schema.GroupVersionKind
	IsNamespaceScoped bool
	Creator           func() runtime.Unstructured
	rest.TableConvertor
}

func (s *BasicStorage) New() runtime.Object {
	obj := s.Creator()
	obj.GetObjectKind().SetGroupVersionKind(s.GVK)
	return obj
}

func (s *BasicStorage) Destroy() {
	// Do nothing
}

func (s *BasicStorage) GroupVersionKind(containingGV schema.GroupVersion) schema.GroupVersionKind {
	return s.GVK
}
func (s *BasicStorage) NamespaceScoped() bool {
	return s.IsNamespaceScoped
}
func (s *BasicStorage) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	return nil, nil
}
func (s *BasicStorage) NewList() runtime.Object {
	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(s.GVK.GroupVersion().WithKind(s.GVK.Kind + "List"))
	return objList
}
func (s *BasicStorage) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return nil, nil
}

type Creater struct {
	*BasicStorage
}

func (s *Creater) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	return nil, nil
}

type Deleter struct {
}

func (s *Deleter) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	return nil, false, nil
}

type Watcher struct {
}

func (s *Watcher) Watch(ctx context.Context, options *metainternal.ListOptions) (watch.Interface, error) {
	return nil, nil
}
