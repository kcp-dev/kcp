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

// GroupObjectsByDefaultHierarchy returns the given objects grouped by their hierarchy using DefaultWeights.
func GroupObjectsByDefaultHierarchy(objects []*unstructured.Unstructured) [][]*unstructured.Unstructured {
	return GroupObjectsByHierarchy(objects, DefaultWeights)
}

// GroupObjectsByHierarchy returns the given objects grouped by their hierarchy as determined by weights.
func GroupObjectsByHierarchy(objects []*unstructured.Unstructured, weights []schema.GroupVersionKind) [][]*unstructured.Unstructured {
	copied := slices.Clone(objects)
	SortObjectsByHierarchy(copied, weights)
	// maximum capacity is everything in weights + catchall
	groups := make([][]*unstructured.Unstructured, 0, len(weights)+1)

	curWeight := -1
	for _, obj := range copied {
		weight := ObjectWeight(obj, weights)
		if weight != curWeight {
			curWeight = weight
			groups = append(groups, []*unstructured.Unstructured{})
		}

		groups[len(groups)-1] = append(groups[len(groups)-1], obj)
	}

	return groups
}

// SortObjectsByDefaultHierarchy sorts the given objects using DefaultWeights.
func SortObjectsByDefaultHierarchy(objects []*unstructured.Unstructured) {
	SortObjectsByHierarchy(objects, DefaultWeights)
}

// SortObjectsByHierarchy ensures that objects are (hopefully) sorted in
// an order that guarantees that they apply without errors and retrying
// for kcp relevant APIs.
//
// Taken from init-agent:
// https://github.com/kcp-dev/init-agent/blob/1e747d414a1bd2417b77bef845f8c68487428890/internal/manifest/sort.go#L27-L60
func SortObjectsByHierarchy(objects []*unstructured.Unstructured, weights []schema.GroupVersionKind) {
	slices.SortFunc(objects, func(objA, objB *unstructured.Unstructured) int {
		weightA := ObjectWeight(objA, weights)
		weightB := ObjectWeight(objB, weights)

		return cmp.Compare(weightA, weightB)
	})
}

// DefaultWeights is the default hierarchy for kcp related APIs to apply
// in order without errors or retrying.
var DefaultWeights = []schema.GroupVersionKind{
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

// ObjectWeight returns the weight of the object based on the given weights.
func ObjectWeight(obj *unstructured.Unstructured, weights []schema.GroupVersionKind) int {
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
