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
	"encoding/json"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
)

// UpsyncDiffAnnotationPrefix is an internal annotation used on downstream resources to specify a transformation
// that should be applied during the Upsyncing of the resource to upstream.
// Format of the annotation is JSONPatch
const UpsyncDiffAnnotationPrefix = "internal.workload.kcp.io/upsyncdiff"

// UpsyncerResourceTransformer defines a very simple transformer which transforms the resource by applying a
// the JSON patch found in the `internal.workload.kcp.io/upsyncdiff` annotation
type UpsyncerResourceTransformer struct{}

func (rt *UpsyncerResourceTransformer) AfterRead(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, upstreamResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (*unstructured.Unstructured, error) {
	return upstreamResource, nil
}

func (rt *UpsyncerResourceTransformer) BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, syncerViewResource *unstructured.Unstructured, subresources ...string) (*unstructured.Unstructured, error) {
	syncTargetKey, err := syncercontext.SyncTargetKeyFrom(ctx)
	if err != nil {
		return nil, err
	}

	diffPatch := syncerViewResource.GetAnnotations()[UpsyncDiffAnnotationPrefix+syncTargetKey]
	if diffPatch == "" {
		return syncerViewResource, nil
	}

	// TODO(jmprusi): hacky way to validate the patch, we should rethink this. Also we should allow some
	// modifications to annotations and labels, but not *all* labels.
	lowerPatch := strings.ToLower(diffPatch)
	if strings.Contains(lowerPatch, "/metadata") || strings.Contains(lowerPatch, "/apiversion") || strings.Contains(lowerPatch, "/kind") {
		return nil, fmt.Errorf("metadata, apiversion or kind cannot be modified by a transformation")
	}

	// TODO(jmprusi): Surface those errors to the user.
	patch, err := jsonpatch.DecodePatch([]byte(diffPatch))
	if err != nil {
		return nil, err
	}

	upstreamResource := syncerViewResource.DeepCopy()
	if err != nil {
		return nil, err
	}
	upstreamResourceJSON, err := json.Marshal(upstreamResource)
	if err != nil {
		return nil, err
	}

	// Apply the patch to the copy of the upstream resource.
	patchedUpstreamResourceJSON, err := patch.Apply(upstreamResourceJSON)
	if err != nil {
		return nil, err
	}
	var newResource *unstructured.Unstructured
	if err := json.Unmarshal(patchedUpstreamResourceJSON, &newResource); err != nil {
		return nil, err
	}

	// Remove the diff annotation.
	annotations := newResource.GetAnnotations()
	delete(annotations, UpsyncDiffAnnotationPrefix+syncTargetKey)
	newResource.SetAnnotations(annotations)
	return newResource, nil
}
