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

package transformations

import (
	"encoding/json"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/sdk/apis/workload/helpers"
	"github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

var _ Transformation = (*SpecDiffTransformation)(nil)
var _ TransformationProvider = (*SpecDiffTransformation)(nil)

type SpecDiffTransformation struct{}

func (t *SpecDiffTransformation) TransformationFor(resource metav1.Object) (Transformation, error) {
	return t, nil
}

func (*SpecDiffTransformation) ToSyncerView(syncTargetKey string, gvr schema.GroupVersionResource, newUpstreamResource *unstructured.Unstructured, overridenSyncerViewFields map[string]interface{}, requestedSyncing map[string]helpers.SyncIntent) (newSyncerViewResource *unstructured.Unstructured, err error) {
	specDiffPatch := newUpstreamResource.GetAnnotations()[v1alpha1.ClusterSpecDiffAnnotationPrefix+syncTargetKey]

	if specDiffPatch == "" {
		return newUpstreamResource, nil
	}

	upstreamSpec, specExists, err := unstructured.NestedFieldCopy(newUpstreamResource.UnstructuredContent(), "spec")
	if err != nil {
		return nil, err
	}
	if !specExists {
		return newUpstreamResource, nil
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
	if err := unstructured.SetNestedMap(newUpstreamResource.UnstructuredContent(), newSpec, "spec"); err != nil {
		return nil, err
	}
	return newUpstreamResource, nil
}
