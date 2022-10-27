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

package dynamic

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

func NewDeleterWithResults(delegate dynamic.ResourceInterface) (DeleterWithResults, error) {
	deleterWithResult, ok := delegate.(DeleterWithResults)
	if ok {
		return deleterWithResult, nil
	}

	dynamicRawDeleter, ok := delegate.(DynamicRawDeleter)
	if !ok {
		return nil, fmt.Errorf("expected a dynamic client that supports raw delete calls, got %T", delegate)
	}

	return &deleterWithResults{delegate: dynamicRawDeleter}, nil
}

type DynamicRawDeleter interface {
	dynamic.ResourceInterface
	RawDeleter
}

type RawDeleter interface {
	RawDelete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) ([]byte, int, error)
	RawDeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOptions metav1.ListOptions) ([]byte, int, error)
}

type DeleterWithResults interface {
	DeleteWithResult(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (*unstructured.Unstructured, int, error)
	DeleteCollectionWithResult(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (*unstructured.UnstructuredList, error)
}

var _ DeleterWithResults = (*deleterWithResults)(nil)

type deleterWithResults struct {
	delegate RawDeleter
}

func (d *deleterWithResults) DeleteWithResult(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (*unstructured.Unstructured, int, error) {
	data, statusCode, err := d.delegate.RawDelete(ctx, name, options, subresources...)
	if err != nil {
		return nil, statusCode, err
	}
	obj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, data)
	if err != nil {
		return nil, statusCode, err
	}

	return obj.(*unstructured.Unstructured), statusCode, nil
}

func (d *deleterWithResults) DeleteCollectionWithResult(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	data, _, err := d.delegate.RawDeleteCollection(ctx, options, listOptions)
	if err != nil {
		return nil, err
	}
	obj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, data)
	if err != nil {
		return nil, err
	}

	return obj.(*unstructured.UnstructuredList), nil
}
