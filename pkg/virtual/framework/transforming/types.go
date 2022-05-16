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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

type TypedTransformers map[schema.GroupVersionResource]Transformers
type Transformers []Transformer

type Transformer struct {
	Name string

	BeforeCreate func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.CreateOptions, []string, error)
	AfterCreate  func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeUpdate func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (context.Context, *unstructured.Unstructured, metav1.UpdateOptions, []string, error)
	AfterUpdate  func(client dynamic.ResourceInterface, ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeDelete           func(client dynamic.ResourceInterface, ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (context.Context, string, metav1.DeleteOptions, []string, error)
	BeforeDeleteCollection func(client dynamic.ResourceInterface, ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (context.Context, metav1.DeleteOptions, metav1.ListOptions, error)

	BeforeGet func(client dynamic.ResourceInterface, ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (context.Context, string, metav1.GetOptions, []string, error)
	AfterGet  func(client dynamic.ResourceInterface, ctx context.Context, name string, options metav1.GetOptions, subresources []string, result *unstructured.Unstructured) (*unstructured.Unstructured, error)

	BeforeList func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
	AfterList  func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions, result *unstructured.UnstructuredList) (*unstructured.UnstructuredList, error)

	BeforeWatch func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions) (context.Context, metav1.ListOptions, error)
	AfterWatch  func(client dynamic.ResourceInterface, ctx context.Context, opts metav1.ListOptions, result watch.Interface) (watch.Interface, error)
}

type TransformingClient struct {
	Transformations Transformers
	Client          dynamic.ResourceInterface
	Resource        schema.GroupVersionResource
	Namespace       string
}

func (tc *TransformingClient) objectCallMessage(transformerName, action string, obj *unstructured.Unstructured, subresources ...string) string {
	name := "nil"
	if obj != nil {
		name = obj.GetName()
	}
	return tc.namedCallMessage(transformerName, action, name, subresources...)
}

func (tc *TransformingClient) namedCallMessage(transformerName, action string, name string, subresources ...string) string {
	return fmt.Sprintf("%s(%s): %s/%s(%s) - %v ", action, transformerName, tc.Namespace, name, tc.Resource, subresources)
}

func (tc *TransformingClient) logObjectCall(transformerName, action string, obj *unstructured.Unstructured, subresources ...string) {
	klog.Info(tc.objectCallMessage(transformerName, action, obj, subresources...))
}

func (tc *TransformingClient) logNamedCall(transformerName, action string, name string, subresources ...string) {
	klog.Info(tc.namedCallMessage(transformerName, action, name, subresources...))
}

func (tc *TransformingClient) logCallError(transformerName, action string, err error) {
	klog.Errorf("Transformation Error on %s(%s): %v", action, transformerName, err)
}

func (tc *TransformingClient) Create(ctx context.Context, obj *unstructured.Unstructured, options metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeCreate
		if action == nil {
			continue
		}
		tc.logObjectCall(transformer.Name, "BeforeCreate", obj, subresources...)
		ctx, obj, options, subresources, err = action(tc.Client, ctx, obj, options, subresources...)
		if err != nil {
			tc.logCallError(transformer.Name, "BeforeCreate", err)
			return nil, err
		}
	}
	result, err := tc.Client.Create(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterCreate
		if action == nil {
			continue
		}
		tc.logObjectCall(transformer.Name, "AfterCreate", obj, subresources...)
		result, err = action(tc.Client, ctx, obj, options, subresources, result)
		if err != nil {
			tc.logCallError(transformer.Name, "AfterCreate", err)
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) Update(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeUpdate
		if action == nil {
			continue
		}
		tc.logObjectCall(transformer.Name, "BeforeUpdate", obj, subresources...)
		ctx, obj, options, subresources, err = action(tc.Client, ctx, obj, options, subresources...)
		if err != nil {
			tc.logCallError(transformer.Name, "BeforeUpdate", err)
			return nil, err
		}
	}
	result, err := tc.Client.Update(ctx, obj, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterUpdate
		if action == nil {
			continue
		}
		tc.logObjectCall(transformer.Name, "AfterUpdate", obj, subresources...)
		result, err = action(tc.Client, ctx, obj, options, subresources, result)
		if err != nil {
			tc.logCallError(transformer.Name, "AfterUpdate", err)
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, options metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}
func (tc *TransformingClient) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return errors.New("not implemented")
}
func (tc *TransformingClient) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return errors.New("not implemented")
}
func (tc *TransformingClient) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeGet
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "BeforeGet", name, subresources...)
		ctx, name, options, subresources, err = action(tc.Client, ctx, name, options, subresources...)
		if err != nil {
			tc.logCallError(transformer.Name, "BeforeGet", err)
			return nil, err
		}
	}
	result, err := tc.Client.Get(ctx, name, options, subresources...)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterGet
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "AfterGet", name, subresources...)
		result, err = action(tc.Client, ctx, name, options, subresources, result)
		if err != nil {
			tc.logCallError(transformer.Name, "AfterGet", err)
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeList
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "BeforeList", opts.LabelSelector)
		ctx, opts, err = action(tc.Client, ctx, opts)
		if err != nil {
			tc.logCallError(transformer.Name, "BeforeList", err)
			return nil, err
		}
	}
	result, err := tc.Client.List(ctx, opts)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterList
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "AfterList", opts.LabelSelector)
		result, err = action(tc.Client, ctx, opts, result)
		if err != nil {
			tc.logCallError(transformer.Name, "AfterList", err)
			return result, err
		}
	}
	return result, err
}
func (tc *TransformingClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var err error
	for _, transformer := range tc.Transformations {
		action := transformer.BeforeWatch
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "BeforeWatch", opts.LabelSelector)
		ctx, opts, err = action(tc.Client, ctx, opts)
		if err != nil {
			tc.logCallError(transformer.Name, "BeforeWatch", err)
			return nil, err
		}
	}
	result, err := tc.Client.Watch(ctx, opts)
	if err != nil {
		return result, err
	}
	for _, transformer := range tc.Transformations {
		action := transformer.AfterWatch
		if action == nil {
			continue
		}
		tc.logNamedCall(transformer.Name, "AfterWatch", opts.LabelSelector)
		result, err = action(tc.Client, ctx, opts, result)
		if err != nil {
			tc.logCallError(transformer.Name, "AfterWatch", err)
			return result, err
		}
	}
	return result, err
}

func (tc *TransformingClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	return nil, errors.New("not implemented")
}

type TransformingWatcher struct {
	source           watch.Interface
	transformedCh    chan watch.Event
	eventTransformer EventTransformer
}

type EventTransformer func(event watch.Event) (transformed watch.Event, skipped bool)

func NewTransformingWatcher(watcher watch.Interface, eventTransformer EventTransformer) *TransformingWatcher {
	tw := &TransformingWatcher{
		source:           watcher,
		transformedCh:    make(chan watch.Event),
		eventTransformer: eventTransformer,
	}
	tw.start()
	return tw
}

func (w *TransformingWatcher) start() {
	go func() {
		for {
			if evt, more := <-w.source.ResultChan(); more {
				transformedEvent, skipped := w.eventTransformer(evt)
				if !skipped {
					w.transformedCh <- transformedEvent
				}
			} else {
				close(w.transformedCh)
				return
			}
		}
	}()
}

// Stop implements Interface
func (w *TransformingWatcher) Stop() {
	w.source.Stop()
}

// ResultChan implements Interface
func (w *TransformingWatcher) ResultChan() <-chan watch.Event {
	return w.transformedCh
}
