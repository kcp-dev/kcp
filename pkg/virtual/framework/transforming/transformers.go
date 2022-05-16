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

package transforming

import (
	"context"
	"errors"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

type UpdateSelectorsFunc func(ctx context.Context, labelSelector labels.Selector, fieldSelector fields.Selector) (labels.Selector, fields.Selector, error)

func TransformsSelectors(transformerName string, updateFunc UpdateSelectorsFunc) Transformer {

	updateSelectorString := func(ctx context.Context, labelSelectorStr string, fieldSelectorStr string) (newLabelSelectorStr string, newFieldSelectorStr string, err error) {
		labelSelector, err := labels.Parse(labelSelectorStr)
		if err != nil {
			return "", "", err
		}
		fieldSelector, err := fields.ParseSelector(fieldSelectorStr)
		if err != nil {
			return "", "", err
		}
		newLabelSelector, newFieldSelector, err := updateFunc(ctx, labelSelector, fieldSelector)
		if err != nil {
			return "", "", err
		}
		newLabelSelectorStr = newLabelSelector.String()
		newFieldSelectorStr = newFieldSelector.String()
		return
	}

	return Transformer{
		Name: transformerName,
		BeforeList: func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error) {
			newLabelSelector, newFieldSelector, err := updateSelectorString(ctx, opts.LabelSelector, opts.FieldSelector)
			if err != nil {
				return ctx, opts, err
			}
			opts.LabelSelector = newLabelSelector
			opts.FieldSelector = newFieldSelector
			return ctx, opts, nil
		},
		BeforeWatch: func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error) {
			newLabelSelector, newFieldSelector, err := updateSelectorString(ctx, opts.LabelSelector, opts.FieldSelector)
			if err != nil {
				return ctx, opts, err
			}
			opts.LabelSelector = newLabelSelector
			opts.FieldSelector = newFieldSelector
			return ctx, opts, nil
		},
	}
}

type TransformResourceBeforeFunc func(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error)
type TransformResourceAfterFunc func(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error)

func TransformsResource(transformerName string, before TransformResourceBeforeFunc, after TransformResourceAfterFunc) Transformer {
	t := Transformer{
		Name: transformerName,
	}

	var loggedAfter func(action string) TransformResourceAfterFunc
	var loggedBefore func(action string) TransformResourceBeforeFunc

	if before != nil {
		loggedBefore = func(action string) TransformResourceBeforeFunc {
			return func(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error) {
				name := "nil"
				if resource != nil {
					name = resource.GetName()
				}
				klog.Infof("Before%s(%s-ResourceTransformation) %s - %v", action, t.Name, name, subresources)
				transformed, err = before(client, ctx, resource, subresources...)
				if err != nil {
					klog.Errorf("%s-ResourceTransformation Error on %s(Before%s): %v", t.Name, name, action, err)
				}
				return transformed, err
			}
		}
	}

	if after != nil {
		loggedAfter = func(action string) TransformResourceAfterFunc {
			return func(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error) {
				name := "nil"
				if resource != nil {
					name = resource.GetName()
				}
				event := ""
				if eventType != nil {
					event = string(*eventType)
				}
				klog.Infof("After%s(%s-ResourceTransformation) %s - %s - %v", action, t.Name, name, event, subresources)
				transformed, err = after(client, ctx, resource, eventType, subresources...)
				if err != nil {
					klog.Errorf("%s-ResourceTransformation Error on %s(After%s): %v", t.Name, name, action, err)
				}
				return transformed, err
			}
		}
	}

	if before != nil {
		t.BeforeCreate = func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.CreateOptions, []string, error) {
			if transformedResource, err := loggedBefore("Create")(client, ctx, obj, subresources...); err != nil {
				return ctx, obj, options, subresources, err
			} else {
				return ctx, transformedResource, options, subresources, nil
			}
		}
		t.BeforeUpdate = func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.UpdateOptions, []string, error) {
			if transformedResource, err := loggedBefore("Update")(client, ctx, obj, subresources...); err != nil {
				return ctx, obj, options, subresources, err
			} else {
				return ctx, transformedResource, options, subresources, nil
			}
		}
	}
	if after != nil {
		t.AfterCreate = func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := loggedAfter("Create")(client, ctx, obj, nil, subresources...); err != nil {
				return obj, err
			} else {
				return transformedResource, nil
			}
		}
		t.AfterUpdate = func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := loggedAfter("Update")(client, ctx, obj, nil, subresources...); err != nil {
				return obj, err
			} else {
				return transformedResource, nil
			}
		}
		// TODO: implement the delete, but for now we don't need it
		t.AfterGet = func(client dynamic.ResourceInterface, ctx context.Context, name string, options metav1.GetOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			if transformedResource, err := loggedAfter("Get")(client, ctx, result, nil, subresources...); err != nil {
				return nil, err
			} else {
				return transformedResource, nil
			}
		}
		t.AfterList = func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions, result *unstructured.UnstructuredList) (*unstructured.UnstructuredList, error) {
			transformedResult := result.DeepCopy()
			transformedResult.Items = []unstructured.Unstructured{}
			afterList := loggedAfter("List")
			for _, item := range result.Items {
				item := item
				if transformed, err := afterList(client, ctx, &item, nil); err != nil {
					if kerrors.IsNotFound(err) {
						continue
					}
				} else {
					transformedResult.Items = append(transformedResult.Items, *transformed)
				}
			}
			return transformedResult, nil
		}
		t.AfterWatch = func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions, result watch.Interface) (watch.Interface, error) {
			afterWatchEvent := loggedAfter("WatchEvent")
			transformingWatcher := NewTransformingWatcher(result, func(event watch.Event) (transformed watch.Event, skipped bool) {
				transformed = event
				eventType := event.Type
				switch event.Type {
				case watch.Added, watch.Modified, watch.Deleted:
					if resource, isUnstructured := event.Object.(*unstructured.Unstructured); !isUnstructured {
						transformed.Type = watch.Error
						transformed.Object = &metav1.Status{
							Status:  "Failure",
							Reason:  metav1.StatusReasonUnknown,
							Message: "Watch expected a resource of type *unstructured.Unstructured",
							Code:    500,
						}
					} else if transformedResource, err := afterWatchEvent(client, ctx, resource, &eventType); err != nil {
						if kerrors.IsNotFound(err) {
							skipped = true
						} else {
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
						}
					} else {
						transformed.Object = transformedResource
					}
				}
				return transformed, skipped
			})
			return transformingWatcher, nil
		}
	}

	return t
}
