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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
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

	clientGetter              ClientGetter
	subResources              []string
	patchConflictRetryBackoff wait.Backoff
}

var _ rest.Lister = &Store{}
var _ rest.Watcher = &Store{}
var _ rest.Getter = &Store{}
var _ rest.Updater = &Store{}

// New implements RESTStorage.New.
func (e *Store) New() runtime.Object {
	return e.NewFunc()
}

// NewList implements rest.Lister.
func (e *Store) NewList() runtime.Object {
	return e.NewListFunc()
}

// GetResetFields implements rest.ResetFieldsStrategy
func (e *Store) GetResetFields() map[fieldpath.APIVersion]*fieldpath.Set {
	if e.ResetFieldsStrategy == nil {
		return nil
	}
	return e.ResetFieldsStrategy.GetResetFields()
}

// List returns a list of items matching labels and field according to the store's PredicateFunc.
func (e *Store) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := e.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.List(ctx, v1ListOptions)
}

// Get implements rest.Getter
func (e *Store) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	delegate, err := e.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Get(ctx, name, *options, e.subResources...)
}

// Watch implements rest.Watcher.
func (e *Store) Watch(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
	var v1ListOptions metav1.ListOptions
	if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
		return nil, err
	}
	delegate, err := e.getClientResource(ctx)
	if err != nil {
		return nil, err
	}

	return delegate.Watch(ctx, v1ListOptions)
}

// Update implements rest.Updater
func (e *Store) Update(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
	delegate, err := e.getClientResource(ctx)
	if err != nil {
		return nil, false, err
	}

	doUpdate := func() (*unstructured.Unstructured, error) {
		oldObj, err := e.Get(ctx, name, &metav1.GetOptions{})
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

		e.updateStrategy.PrepareForUpdate(ctx, obj, oldObj)
		if errs := e.updateStrategy.ValidateUpdate(ctx, obj, oldObj); len(errs) > 0 {
			return nil, kerrors.NewInvalid(unstructuredObj.GroupVersionKind().GroupKind(), unstructuredObj.GetName(), errs)
		}
		if err := updateValidation(ctx, obj.DeepCopyObject(), oldObj.DeepCopyObject()); err != nil {
			return nil, err
		}

		return delegate.Update(ctx, unstructuredObj, *options, e.subResources...)
	}

	requestInfo, _ := genericapirequest.RequestInfoFrom(ctx)
	if requestInfo != nil && requestInfo.Verb == "patch" {
		var result *unstructured.Unstructured
		err := retry.RetryOnConflict(e.patchConflictRetryBackoff, func() error {
			var err error
			result, err = doUpdate()
			return err
		})
		return result, false, err
	}

	result, err := doUpdate()
	return result, false, err
}

func (e *Store) getClientResource(ctx context.Context) (dynamic.ResourceInterface, error) {
	client, err := s.clientGetter.GetDynamicClient(ctx)
	if err != nil {
		return nil, err
	}

	if s.createStrategy.NamespaceScoped() {
		if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
			return client.Resource(s.resource).Namespace(namespace), nil
		} else {
			return nil, fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", s.resource.String())
		}
	} else {
		return client.Resource(s.resource), nil
	}
}
