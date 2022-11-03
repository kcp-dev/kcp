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

	jsonpatch "github.com/evanphx/json-patch"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
)

const UpsyncSpecDiffAnnotationPrefix = "spec-diff.upsync.workload.kcp.dev/"

type UpsyncerResourceTransformer struct{}

func (rt *UpsyncerResourceTransformer) AfterRead(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, upstreamResource *unstructured.Unstructured, eventType *watch.EventType, subresources ...string) (*unstructured.Unstructured, error) {
	return upstreamResource, nil
}

func (rt *UpsyncerResourceTransformer) BeforeWrite(client dynamic.ResourceInterface, ctx context.Context, gvr schema.GroupVersionResource, syncerViewResource *unstructured.Unstructured, subresources ...string) (*unstructured.Unstructured, error) {
	syncTargetKey, err := syncercontext.SyncTargetKeyFrom(ctx)
	if err != nil {
		return nil, err
	}

	specDiffPatch := syncerViewResource.GetAnnotations()[UpsyncSpecDiffAnnotationPrefix+syncTargetKey]
	if specDiffPatch == "" {
		return syncerViewResource, nil
	}

	upstreamSpec, specExists, err := unstructured.NestedFieldCopy(syncerViewResource.UnstructuredContent(), "spec")
	if err != nil {
		return nil, err
	}
	if !specExists {
		return syncerViewResource, nil
	}

	// TODO(jmprusi): Surface those errors to the user.
	patch, err := jsonpatch.DecodePatch([]byte(specDiffPatch))
	if err != nil {
		return nil, err
	}
	upstreamSpecJSON, err := json.Marshal(upstreamSpec)
	if err != nil {
		return nil, err
	}
	patchedUpstreamSpecJSON, err := patch.Apply(upstreamSpecJSON)
	if err != nil {
		return nil, err
	}
	var newSpec map[string]interface{}
	if err := json.Unmarshal(patchedUpstreamSpecJSON, &newSpec); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedMap(syncerViewResource.UnstructuredContent(), newSpec, "spec"); err != nil {
		return nil, err
	}
	return syncerViewResource, nil
}
