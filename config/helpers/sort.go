/*
Copyright 2026 The KCP Authors.

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

package helpers

import (
	"cmp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ChunkObjectsByHierarchy returns the given objects chunked by their
// hierarchy as noted in SortObjectsByHierarchy.
// The objects slice is modified in-place.
func ChunkObjectsByHierarchy(objects []*unstructured.Unstructured) [][]*unstructured.Unstructured {
	SortObjectsByHierarchy(objects)
	chunks := make([][]*unstructured.Unstructured, 0, len(weights))

	curWeight := -1
	for _, obj := range objects {
		weight := objectWeight(obj)
		if weight != curWeight {
			curWeight = weight
			chunks = append(chunks, []*unstructured.Unstructured{})
		}

		chunks[len(chunks)-1] = append(chunks[len(chunks)-1], obj)
	}

	return chunks
}

// SortObjectsByHierarchy ensures that objects are sorted in the following order:
//
// 1. CRDs
// 2. APIExports
// 3. APIBindings
// 4. Namespaces
// 5. <everything else>
//
// This ensures they can be successfully applied in order (though some delay
// might be required between creating a CRD and creating objects using that CRD).
//
// Taken from init-agent:
// https://github.com/kcp-dev/init-agent/blob/1e747d414a1bd2417b77bef845f8c68487428890/internal/manifest/sort.go#L27-L60
func SortObjectsByHierarchy(objects []*unstructured.Unstructured) {
	slices.SortFunc(objects, func(objA, objB *unstructured.Unstructured) int {
		weightA := objectWeight(objA)
		weightB := objectWeight(objB)

		return cmp.Compare(weightA, weightB)
	})
}

var weights = []string{
	"customresourcedefinition.apiextensions.k8s.io",
	"logicalcluster.core.kcp.io",
	"apiexport.apis.kcp.io",
	"apibinding.apis.kcp.io",
	"namespace",
}

func groupKind(obj *unstructured.Unstructured) string {
	return strings.ToLower(obj.GroupVersionKind().GroupKind().String())
}

func objectWeight(obj *unstructured.Unstructured) int {
	weight := slices.Index(weights, groupKind(obj))
	if weight == -1 {
		weight = len(weights)
	}

	return weight
}
