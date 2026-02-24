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

package helpers

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ChunkObjectsByHierarchy returns the given objects chunked by their
// hierarchy as noted in SortObjectsByHierarchy.
// The objects slice is modified in-place.
func ChunkObjectsByHierarchy(objects []*unstructured.Unstructured) [][]*unstructured.Unstructured {
	SortObjectsByHierarchy(objects)
	// maximum capacity is everything in weights + catchall
	chunks := make([][]*unstructured.Unstructured, 0, len(weights)+1)

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

// SortObjectsByHierarchy ensures that objects are (hopefully) sorted in
// an order that guarantees that they apply without errors and retrying
// for kcp relevant APIs.
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

var weights = []schema.GroupVersionKind{
	{Group: "apiextensions.k8s.io"},

	{Group: "core.kcp.io"},
	{Group: "tenancy.kcp.io", Kind: "WorkspaceAuthenticationConfiguration"}, // WorkspaceAuthenticationConfigurations are used by WorkspaceTypes
	{Group: "tenancy.kcp.io", Kind: "WorkspaceType"},                        // WorkspaceTypes are used by Workspaces
	{Group: "tenancy.kcp.io"},
	{Group: "topology.kcp.io"},
	{Group: "apis.kcp.io", Kind: "APIResourceSchema"}, // APIResourceSchemas are used by APIExports
	{Group: "apis.kcp.io", Kind: "APIExport"},         // APIExports are used by APIBindings
	{Group: "apis.kcp.io", Kind: "APIBinding"},
	{Group: "apis.kcp.io"},

	{Kind: "Namespace"},
}

func objectWeight(obj *unstructured.Unstructured) int {
	for weight, gvk := range weights {
		// Ignoring versions entirely.
		objGvk := obj.GroupVersionKind()
		switch {
		case gvk.Group != "" && gvk.Kind != "":
			if gvk.Group == objGvk.Group && gvk.Kind == objGvk.Kind {
				return weight
			}
		case gvk.Group != "":
			if gvk.Group == objGvk.Group {
				return weight
			}
		case gvk.Kind != "":
			if gvk.Kind == objGvk.Kind {
				return weight
			}
		}
	}
	return len(weights)
}
