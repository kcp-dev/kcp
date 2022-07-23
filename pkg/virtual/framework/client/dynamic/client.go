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

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

var (
	versionV1 = schema.GroupVersion{Version: "v1"}

	deleteScheme          = runtime.NewScheme()
	parameterScheme       = runtime.NewScheme()
	deleteOptionsCodec    = serializer.NewCodecFactory(deleteScheme)
	dynamicParameterCodec = runtime.NewParameterCodec(parameterScheme)
)

func init() {
	metav1.AddToGroupVersion(parameterScheme, versionV1)
	metav1.AddToGroupVersion(deleteScheme, versionV1)
}

type ResourceInterface interface {
	dynamic.ResourceInterface
	ResourceDeleterInterface
}

type ResourceDeleterInterface interface {
	DeleteWithResult(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (*unstructured.Unstructured, int, error)
	DeleteCollectionWithResult(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (*unstructured.UnstructuredList, error)
}

func NewClusterForConfig(c *rest.Config) (*Cluster, error) {
	delegate, err := dynamic.NewClusterForConfig(c)
	if err != nil {
		return nil, err
	}

	config := dynamic.ConfigFor(c)
	config.GroupVersion = &schema.GroupVersion{}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}
	restClient, err := rest.RESTClientForConfigAndClient(config, httpClient)
	if err != nil {
		return nil, err
	}

	return &Cluster{delegate: delegate, scopedClient: &scopedClient{restClient}}, nil
}

type Cluster struct {
	*scopedClient
	delegate *dynamic.Cluster
}

func (c *Cluster) Cluster(name logicalcluster.Name) dynamic.Interface {
	return &dynamicClient{
		delegate:     c.delegate.Cluster(name),
		scopedClient: c.scopedClient,
		cluster:      name,
	}
}

type dynamicClient struct {
	*scopedClient
	delegate dynamic.Interface
	cluster  logicalcluster.Name
}

type scopedClient struct {
	client *rest.RESTClient
}

var _ dynamic.Interface = &dynamicClient{}

type dynamicResourceClient struct {
	dynamic.ResourceInterface
	delegate  dynamic.Interface
	client    *rest.RESTClient
	cluster   logicalcluster.Name
	namespace string
	resource  schema.GroupVersionResource
}

var _ ResourceInterface = &dynamicResourceClient{}

func (c *dynamicClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &dynamicResourceClient{
		ResourceInterface: c.delegate.Resource(resource),
		delegate:          c.delegate,
		client:            c.client,
		cluster:           c.cluster,
		resource:          resource,
	}
}

func (c *dynamicResourceClient) Namespace(ns string) dynamic.ResourceInterface {
	ret := *c
	ret.namespace = ns
	ret.ResourceInterface = c.delegate.Resource(c.resource).Namespace(ns)
	return &ret
}

func (c *dynamicResourceClient) DeleteWithResult(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) (*unstructured.Unstructured, int, error) {
	statusCode := 0

	if len(name) == 0 {
		return nil, statusCode, fmt.Errorf("name is required")
	}

	deleteOptionsByte, err := runtime.Encode(deleteOptionsCodec.LegacyCodec(schema.GroupVersion{Version: "v1"}), &options)
	if err != nil {
		return nil, statusCode, err
	}

	result := c.client.
		Delete().
		Cluster(c.cluster).
		AbsPath(append(c.makeURLSegments(name), subresources...)...).
		SetHeader("Content-Type", runtime.ContentTypeJSON).
		Body(deleteOptionsByte).
		Do(ctx).
		StatusCode(&statusCode)

	if err := result.Error(); err != nil {
		return nil, statusCode, err
	}

	retBytes, err := result.Raw()
	if err != nil {
		return nil, statusCode, err
	}
	obj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, retBytes)
	if err != nil {
		return nil, statusCode, err
	}

	return obj.(*unstructured.Unstructured), statusCode, nil
}

func (c *dynamicResourceClient) DeleteCollectionWithResult(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	deleteOptionsByte, err := runtime.Encode(deleteOptionsCodec.LegacyCodec(schema.GroupVersion{Version: "v1"}), &options)
	if err != nil {
		return nil, err
	}

	result := c.client.
		Delete().
		Cluster(c.cluster).
		AbsPath(c.makeURLSegments("")...).
		SetHeader("Content-Type", runtime.ContentTypeJSON).
		Body(deleteOptionsByte).
		SpecificallyVersionedParams(&listOptions, dynamicParameterCodec, versionV1).
		Do(ctx)

	if err := result.Error(); err != nil {
		return nil, err
	}

	retBytes, err := result.Raw()
	if err != nil {
		return nil, err
	}
	obj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, retBytes)
	if err != nil {
		return nil, err
	}

	return obj.(*unstructured.UnstructuredList), nil
}

func (c *dynamicResourceClient) makeURLSegments(name string) []string {
	var url []string
	if len(c.resource.Group) == 0 {
		url = append(url, "api")
	} else {
		url = append(url, "apis", c.resource.Group)
	}
	url = append(url, c.resource.Version)

	if len(c.namespace) > 0 {
		url = append(url, "namespaces", c.namespace)
	}
	url = append(url, c.resource.Resource)

	if len(name) > 0 {
		url = append(url, name)
	}

	return url
}
