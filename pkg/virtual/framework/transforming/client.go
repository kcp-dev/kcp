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

package transforming

import (
	"context"
	"errors"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

type transformingResourceClient struct {
	transformer ResourceTransformer
	client      dynamic.ResourceInterface
	resource    schema.GroupVersionResource
	namespace   string
}

func (tc *transformingResourceClient) objectCallMessage(action string, obj *unstructured.Unstructured, subresources ...string) string {
	name := "nil"
	if obj != nil {
		name = obj.GetName()
	}
	return tc.namedCallMessage(action, name, subresources...)
}

func (tc *transformingResourceClient) namedCallMessage(action string, name string, subresources ...string) string {
	return fmt.Sprintf("%s: %s/%s(%s) - %v ", action, tc.namespace, name, tc.resource, subresources)
}

func (tc *transformingResourceClient) logObjectCall(action string, obj *unstructured.Unstructured, subresources ...string) {
	klog.Info(tc.objectCallMessage(action, obj, subresources...))
}

func (tc *transformingResourceClient) logNamedCall(action string, name string, subresources ...string) {
	klog.Info(tc.namedCallMessage(action, name, subresources...))
}

func (tc *transformingResourceClient) logCallError(action string, err error) {
	klog.Errorf("Transformation Error on %s: %v", action, err)
}

func (tc *transformingResourceClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	tc.logObjectCall("BeforeCreate", obj, subresources...)
	obj, err = tc.transformer.BeforeWrite(tc.client, ctx, obj, subresources...)
	if err != nil {
		tc.logCallError("BeforeCreate", err)
		return nil, err
	}
	result, err := tc.client.Create(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	tc.logObjectCall("AfterCreate", obj, subresources...)
	result, err = tc.transformer.AfterRead(tc.client, ctx, result, nil, subresources...)
	if err != nil {
		tc.logCallError("AfterCreate", err)
		return result, err
	}
	return result, err
}
func (tc *transformingResourceClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	tc.logObjectCall("BeforeUpdate", obj, subresources...)
	obj, err = tc.transformer.BeforeWrite(tc.client, ctx, obj, subresources...)
	if err != nil {
		tc.logCallError("BeforeUpdate", err)
		return nil, err
	}
	result, err := tc.client.Update(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	tc.logObjectCall("AfterUpdate", obj, subresources...)
	result, err = tc.transformer.AfterRead(tc.client, ctx, result, nil, subresources...)
	if err != nil {
		tc.logCallError("AfterUpdate", err)
		return result, err
	}
	return result, err
}
func (tc *transformingResourceClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	result, err := tc.client.Get(ctx, name, options, subresources...)
	if err != nil {
		return result, err
	}
	tc.logNamedCall("AfterGet", name, subresources...)
	result, err = tc.transformer.AfterRead(tc.client, ctx, result, nil, subresources...)
	if err != nil {
		tc.logCallError("AfterGet", err)
		return result, err
	}
	return result, err
}
func (tc *transformingResourceClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	var err error
	result, err := tc.client.List(ctx, opts)
	if err != nil {
		return result, err
	}

	tc.logNamedCall("AfterList", opts.LabelSelector)
	transformedResult := result.DeepCopy()
	transformedResult.Items = []unstructured.Unstructured{}
	for _, item := range result.Items {
		item := item
		tc.logNamedCall("AfterListItem", item.GetName())
		if transformed, err := tc.transformer.AfterRead(tc.client, ctx, &item, nil); err != nil {
			if kerrors.IsNotFound(err) {
				continue
			}
			tc.logCallError("AfterList", err)
			return nil, err
		} else {
			transformedResult.Items = append(transformedResult.Items, *transformed)
		}
	}
	return transformedResult, nil
}
func (tc *transformingResourceClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var err error
	result, err := tc.client.Watch(ctx, opts)
	if err != nil {
		return result, err
	}

	tc.logNamedCall("AfterWatch", opts.LabelSelector)
	transformingWatcher := NewTransformingWatcher(result, func(event watch.Event) *watch.Event {
		transformed := event
		eventType := event.Type
		if eventType == watch.Bookmark || eventType == watch.Error {
			return &transformed
		}
		resource, ok := event.Object.(*unstructured.Unstructured)
		if !ok {
			transformed.Type = watch.Error
			transformed.Object = &metav1.Status{
				Status:  "Failure",
				Reason:  metav1.StatusReasonUnknown,
				Message: "Watch expected a resource of type *unstructured.Unstructured",
				Code:    500,
			}
			return &transformed
		}
		tc.logNamedCall("AfterWatchItem", string(eventType)+"/"+resource.GetName())
		if transformedResource, err := tc.transformer.AfterRead(tc.client, ctx, resource, &eventType); err != nil {
			if kerrors.IsNotFound(err) {
				return nil
			}
			transformed.Type = watch.Error
			statusError := &kerrors.StatusError{}
			if errors.As(err, &statusError) {
				transformed.Object = statusError.ErrStatus.DeepCopy()
			} else {
				transformed.Object = &metav1.Status{
					Status:  "Failure",
					Reason:  metav1.StatusReasonUnknown,
					Message: "Watch transformation failed",
					Code:    500,
					Details: &metav1.StatusDetails{
						Name:  resource.GetName(),
						Group: resource.GroupVersionKind().Group,
						Kind:  resource.GroupVersionKind().Kind,
						Causes: []metav1.StatusCause{
							{
								Type:    metav1.CauseTypeUnexpectedServerResponse,
								Message: err.Error(),
							},
						},
					},
				}
			}
		} else {
			transformed.Object = transformedResource
		}
		return &transformed
	})
	return transformingWatcher, nil
}

func (tc *transformingResourceClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return tc.Update(ctx, obj, options, "status")
}

func (tc *transformingResourceClient) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return tc.client.Delete(ctx, name, options, subresources...)
}

func (tc *transformingResourceClient) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return tc.client.DeleteCollection(ctx, options, listOptions)
}

func (tc *transformingResourceClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}
