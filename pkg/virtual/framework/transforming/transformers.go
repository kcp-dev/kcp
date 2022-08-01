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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

type ResourceTransformer interface {
	BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error)
	AfterRead(client dynamic.ResourceInterface, ctx context.Context, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error)
}

func WithResourceTransformer(clusterClient dynamic.ClusterInterface, transformer ResourceTransformer) dynamic.ClusterInterface {
	return &transformingClusterClient{
		transformer:   transformer,
		clusterClient: clusterClient,
	}
}

type transformingClusterClient struct {
	transformer   ResourceTransformer
	clusterClient dynamic.ClusterInterface
}

func (c *transformingClusterClient) Cluster(name logicalcluster.Name) dynamic.Interface {
	return &transformingClient{
		transformer: c.transformer,
		client:      c.clusterClient.Cluster(name),
	}
}

type transformingClient struct {
	transformer ResourceTransformer
	client      dynamic.Interface
}

func (c *transformingClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &transformingNamespaceableResourceClient{
		transformer:                    c.transformer,
		namespaceableResourceInterface: c.client.Resource(resource),
		ResourceInterface: &transformingResourceClient{
			transformer: c.transformer,
			client:      c.client.Resource(resource),
			resource:    resource,
		},
		resource: resource,
	}
}

type transformingNamespaceableResourceClient struct {
	transformer                    ResourceTransformer
	namespaceableResourceInterface dynamic.NamespaceableResourceInterface
	dynamic.ResourceInterface
	resource schema.GroupVersionResource
}

func (c *transformingNamespaceableResourceClient) Namespace(namespace string) dynamic.ResourceInterface {
	return &transformingResourceClient{
		transformer: c.transformer,
		client:      c.namespaceableResourceInterface.Namespace(namespace),
		resource:    c.resource,
		namespace:   namespace,
	}
}
