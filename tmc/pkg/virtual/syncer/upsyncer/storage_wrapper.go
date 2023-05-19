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

package upsyncer

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

// WithStaticLabelSelectorAndInWriteCallsCheck returns a StorageWrapper that adds the given label selector to the reading calls
// (Get, List and Watch), but also checks that write calls (Create or Update) are refused with an error if the resource
// would not be matched by the given label selector.
func WithStaticLabelSelectorAndInWriteCallsCheck(labelSelector labels.Requirements) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(
		func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
			delegateCreater := storage.CreaterFunc
			storage.CreaterFunc = func(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
				if meta, ok := obj.(metav1.Object); ok {
					if !labels.Everything().Add(labelSelector...).Matches(labels.Set(meta.GetLabels())) {
						return nil, apierrors.NewBadRequest(fmt.Sprintf("label selector %q does not match labels %v", labelSelector, meta.GetLabels()))
					}
				}
				return delegateCreater.Create(ctx, obj, createValidation, options)
			}

			delegateUpdater := storage.UpdaterFunc
			storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
				// Note, we have to pass in a non-nil value for oldObj. Ideally it would be the zero value of the
				// appropriate type (e.g a built-in type such as corev1.Namespace, or Unstructured for a custom resource).
				// Unfortunately we don't know what the appropriate type is here, so we're using Unstructured. The
				// transformers called by UpdatedObject should only be acting on things that satisfy the ObjectMeta
				// interface, so this should be ok.
				obj, err := objInfo.UpdatedObject(ctx, &unstructured.Unstructured{})
				if apierrors.IsNotFound(err) {
					return delegateUpdater.Update(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
				}
				if err != nil {
					return nil, false, err
				}

				if meta, ok := obj.(metav1.Object); ok {
					if !labels.Everything().Add(labelSelector...).Matches(labels.Set(meta.GetLabels())) {
						return nil, false, apierrors.NewBadRequest(fmt.Sprintf("label selector %q does not match labels %v", labelSelector, meta.GetLabels()))
					}
				}
				return delegateUpdater.Update(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
			}

			staticStorage := forwardingregistry.WithStaticLabelSelector(labelSelector)
			staticStorage.Decorate(resource, storage)
		},
	)
}
