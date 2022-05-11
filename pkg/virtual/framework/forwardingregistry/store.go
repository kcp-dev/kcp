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
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

// ClientGetter provides a way to get a dynamic client based on a given context.
// It is used to forward REST requests to a client that depends on the request context.
type ClientGetter interface {
	GetDynamicClient(ctx context.Context) (dynamic.Interface, error)
}

type Store struct {
	// NewFunc returns a new instance of the type this registry returns for a
	// GET of a single object, e.g.:
	//
	// curl GET /apis/group/version/namespaces/my-ns/myresource/name-of-object
	NewFunc func() runtime.Object

	// NewListFunc returns a new list of the type this registry; it is the
	// type returned when the resource is listed, e.g.:
	//
	// curl GET /apis/group/version/namespaces/my-ns/myresource
	NewListFunc func() runtime.Object

	// DefaultQualifiedResource is the pluralized name of the resource.
	// This field is used if there is no request info present in the context.
	// See qualifiedResourceFromContext for details.
	DefaultQualifiedResource schema.GroupResource

	// CreateStrategy implements resource-specific behavior during creation.
	CreateStrategy rest.RESTCreateStrategy

	// UpdateStrategy implements resource-specific behavior during updates.
	UpdateStrategy rest.RESTUpdateStrategy

	// DeleteStrategy implements resource-specific behavior during deletion.
	DeleteStrategy rest.RESTDeleteStrategy

	// TableConvertor is an optional interface for transforming items or lists
	// of items into tabular output. If unset, the default will be used.
	TableConvertor rest.TableConvertor

	// ResetFieldsStrategy provides the fields reset by the strategy that
	// should not be modified by the user.
	ResetFieldsStrategy rest.ResetFieldsStrategy

	resource                  schema.GroupVersionResource
	clientGetter              ClientGetter
	subResources              []string
	patchConflictRetryBackoff wait.Backoff
}

var _ rest.StandardStorage = &Store{}

// New implements RESTStorage.New.
func (s *Store) New() runtime.Object {
	return s.NewFunc()
}

// NewList implements rest.Lister.
func (s *Store) NewList() runtime.Object {
	return s.NewListFunc()
}

// GetResetFields implements rest.ResetFieldsStrategy
func (s *Store) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	if s.ResetFieldsStrategy == nil {
		return nil
	}
	return s.ResetFieldsStrategy.GetResetFields()
}

// List returns a list of items matching labels and field according to the store's PredicateFunc.
func (s *Store) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := s.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.List(ctx, v1ListOptions)
}

// Get implements rest.Getter
func (s *Store) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	delegate, err := s.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Get(ctx, name, *options, s.subResources...)
}

// Watch implements rest.Watcher.
func (s *Store) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := s.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Watch(ctx, v1ListOptions)
}

// Update implements rest.Updater
func (s *Store) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	delegate, err := s.getClientResource(ctx)
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

		s.UpdateStrategy.PrepareForUpdate(ctx, obj, oldObj)
		if errs := s.UpdateStrategy.ValidateUpdate(ctx, obj, oldObj); len(errs) > 0 {
			return nil, kerrors.NewInvalid(unstructuredObj.GroupVersionKind().GroupKind(), unstructuredObj.GetName(), errs)
		}
		if err := updateValidation(ctx, obj.DeepCopyObject(), oldObj.DeepCopyObject()); err != nil {
			return nil, err
		}

		return delegate.Update(ctx, unstructuredObj, *options, s.subResources...)
	}

	requestInfo, _ := genericapirequest.RequestInfoFrom(ctx)
	if requestInfo != nil && requestInfo.Verb == "patch" {
		var result *unstructured.Unstructured
		err := retry.RetryOnConflict(s.patchConflictRetryBackoff, func() error {
			var err error
			result, err = doUpdate()
			return err
		})
		return result, false, err
	}

	result, err := doUpdate()
	return result, false, err
}

func (s *Store) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	//TODO implement me
	panic("implement me")
}

func (s *Store) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	//TODO implement me
	panic("implement me")
}

func (s *Store) DeleteCollection(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
	//TODO implement me
	panic("implement me")
}

func (s *Store) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return s.TableConvertor.ConvertToTable(ctx, object, tableOptions)
}

func (s *Store) getClientResource(ctx context.Context) (dynamic.ResourceInterface, error) {
	client, err := s.clientGetter.GetDynamicClient(ctx)
	if err != nil {
		return nil, err
	}

	if s.CreateStrategy.NamespaceScoped() {
		if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
			return client.Resource(s.resource).Namespace(namespace), nil
		} else {
			return nil, fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", s.resource.String())
		}
	} else {
		return client.Resource(s.resource), nil
	}
}
