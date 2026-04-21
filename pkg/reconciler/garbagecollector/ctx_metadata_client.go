/*
Copyright 2026 The kcp Authors.

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

package garbagecollector

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/metadata"
	"k8s.io/kubernetes/pkg/controller/garbagecollector"

	kcpmetadataclient "github.com/kcp-dev/client-go/metadata"
)

var _ metadata.Interface = (*ctxMetadataClient)(nil)

// ctxMetadataClient is a shim around the cluster-aware metadataclient.
// It extracts the intended cluster from the context and then passes the
// request to the correct scoped client.
// It is used to give the upstream garbage collector a cluster-aware
// metadata client without requiring (many) code changes to client
// handling.
type ctxMetadataClient struct {
	client kcpmetadataclient.ClusterInterface
}

func newCtxMetadataClient(client kcpmetadataclient.ClusterInterface) metadata.Interface {
	return &ctxMetadataClient{client: client}
}

func (c *ctxMetadataClient) Resource(resource schema.GroupVersionResource) metadata.Getter {
	return &ctxMetadataGetter{client: c.client, resource: resource}
}

type ctxMetadataGetter struct {
	client    kcpmetadataclient.ClusterInterface
	resource  schema.GroupVersionResource
	namespace string
}

func (g *ctxMetadataGetter) Namespace(ns string) metadata.ResourceInterface {
	return &ctxMetadataGetter{client: g.client, resource: g.resource, namespace: ns}
}

func (g *ctxMetadataGetter) scopedClient(ctx context.Context) metadata.ResourceInterface {
	cluster := garbagecollector.ClusterFrom(ctx)
	c := g.client.Cluster(cluster.Path()).Resource(g.resource)
	if g.namespace != "" {
		return c.Namespace(g.namespace)
	}
	return c
}

func (g *ctxMetadataGetter) Delete(ctx context.Context, name string, options metav1.DeleteOptions, subresources ...string) error {
	return g.scopedClient(ctx).Delete(ctx, name, options, subresources...)
}

func (g *ctxMetadataGetter) DeleteCollection(ctx context.Context, options metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	return g.scopedClient(ctx).DeleteCollection(ctx, options, listOptions)
}

func (g *ctxMetadataGetter) Get(ctx context.Context, name string, options metav1.GetOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	return g.scopedClient(ctx).Get(ctx, name, options, subresources...)
}

func (g *ctxMetadataGetter) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return g.scopedClient(ctx).List(ctx, opts)
}

func (g *ctxMetadataGetter) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return g.scopedClient(ctx).Watch(ctx, opts)
}

func (g *ctxMetadataGetter) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, options metav1.PatchOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	return g.scopedClient(ctx).Patch(ctx, name, pt, data, options, subresources...)
}
