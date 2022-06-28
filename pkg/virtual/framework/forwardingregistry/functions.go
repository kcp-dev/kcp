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

	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/registry/rest"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

type FactoryFunc func() runtime.Object

func (f FactoryFunc) New() runtime.Object {
	return f()
}

type DestroyerFunc func()

func (f DestroyerFunc) Destroy() {
	f()
}

var _ rest.Storage = &struct {
	FactoryFunc
	DestroyerFunc
}{}

type CreaterFunc func(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error)

func (f CreaterFunc) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	return f(ctx, obj, createValidation, options)
}

var _ rest.Creater = &struct {
	FactoryFunc
	CreaterFunc
}{}

type GetterFunc func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error)

func (f GetterFunc) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	return f(ctx, name, options)
}

var _ rest.Getter = GetterFunc(nil)

type TableConvertorFunc func(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error)

func (f TableConvertorFunc) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return f(ctx, object, tableOptions)
}

var _ rest.TableConvertor = TableConvertorFunc(nil)

type GracefulDeleterFunc func(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error)

func (f GracefulDeleterFunc) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	return f(ctx, name, deleteValidation, options)
}

var _ rest.GracefulDeleter = GracefulDeleterFunc(nil)

type CollectionDeleterFunc func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error)

func (f CollectionDeleterFunc) DeleteCollection(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
	return f(ctx, deleteValidation, options, listOptions)
}

var _ rest.CollectionDeleter = CollectionDeleterFunc(nil)

type UpdaterFunc func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error)

func (f UpdaterFunc) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	return f(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
}

var _ rest.Updater = &struct {
	FactoryFunc
	UpdaterFunc
}{}

type WatcherFunc func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error)

func (f WatcherFunc) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	return f(ctx, options)
}

var _ rest.Watcher = WatcherFunc(nil)

type ListFactoryFunc func() runtime.Object

func (f ListFactoryFunc) NewList() runtime.Object {
	return f()
}

type ListerFunc func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error)

func (f ListerFunc) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	return f(ctx, options)
}

var _ rest.Lister = &struct {
	ListFactoryFunc
	ListerFunc
	TableConvertorFunc
}{}

type CategoriesProviderFunc func() []string

func (f CategoriesProviderFunc) Categories() []string {
	return f()
}

var _ rest.CategoriesProvider = CategoriesProviderFunc(nil)

type ResetFieldsStrategyFunc func() map[fieldpath.APIVersion]*fieldpath.Set

func (f ResetFieldsStrategyFunc) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	return f()
}

var _ rest.ResetFieldsStrategy = ResetFieldsStrategyFunc(nil)
