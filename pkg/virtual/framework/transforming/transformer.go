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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

// ResourceTransformer define transformations that should be applied to a resource
// before and after a client submits a resource request to a kubernetes client.
type ResourceTransformer interface {
	// BeforeWrite is called before a resource is created or updated. The resource effectively created or updated
	// sent to the kubernetes client is the transformed resource returned as result of this method call.
	BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, resource *unstructured.Unstructured, subresources ...string) (transformed *unstructured.Unstructured, err error)

	// AfterRead is called after a resource is returned from a kubernetes client call.
	// This includes Get and List calls, but also Create, and Update (since they return the created or updated resource).
	// It is also called for every resource associated to Watch Events in a Watch call.
	// In all those cases, the resource effectively read is the transformed resource returned as result of this method call.
	AfterRead(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, resource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (transformed *unstructured.Unstructured, err error)
}

// WithResourceTransformer returns a ClusterInterface client whose kubernetes clients are wired with a ResourceTransformer.
func WithResourceTransformer(clusterClient kcpdynamic.ClusterInterface, transformer ResourceTransformer) kcpdynamic.ClusterInterface {
	return &transformingDynamicClusterClient{
		transformer: transformer,
		delegate:    clusterClient,
	}
}
