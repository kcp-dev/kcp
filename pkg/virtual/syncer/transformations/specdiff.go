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
	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ SyncerTransformation = (*SpecDiffTransformation)(nil)

type SpecDiffTransformation struct{}

func (*SpecDiffTransformation) ReadFromKCP(syncTargetName string, newKCPResource, existingSyncerViewResource *unstructured.Unstructured, requestedSyncing map[string]SyncTargetSyncing) (*unstructured.Unstructured, error) {
	specDiffPatch := newKCPResource.GetAnnotations()[v1alpha1.ClusterSpecDiffAnnotationPrefix+syncTargetName]

	if specDiffPatch == "" {
		return newKCPResource, nil
	}

	upstreamSpec, specExists, err := unstructured.NestedFieldCopy(newKCPResource.UnstructuredContent(), "spec")
	if err != nil {
		return nil, err
	}
	if !specExists {
		return newKCPResource, nil
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
	if err := unstructured.SetNestedMap(newKCPResource.UnstructuredContent(), newSpec, "spec"); err != nil {
		return nil, err
	}
	return newKCPResource, nil
}
