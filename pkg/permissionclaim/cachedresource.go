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

package permissionclaim

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

// clusterCachedResourceEndpointSliceKind is the kind an APIExport resource entry
// references when the resource is served from a ClusterCachedResource.
const clusterCachedResourceEndpointSliceKind = "ClusterCachedResourceEndpointSlice"

// ServedFromClusterCachedResource reports whether export serves gr from a
// ClusterCachedResource.
//
// Such a resource is not stored per consumer: it is one read-only copy,
// replicated to every workspace that binds the export. That has two
// consequences for a permission claim on it. The claim label controller only
// labels objects in a consumer workspace, so a replicated object never carries a
// claim label; and there is no per-consumer object to attach a per-consumer
// selector to, so a claim that narrows by label cannot be honoured at all.
//
// A nil export reports false, so a claim on a built-in or on a resource whose
// producer this shard cannot see is treated as an ordinary claim.
func ServedFromClusterCachedResource(export *apisv1alpha2.APIExport, gr schema.GroupResource) bool {
	if export == nil {
		return false
	}
	return slices.ContainsFunc(export.Spec.Resources, func(res apisv1alpha2.ResourceSchema) bool {
		// Only a whole resource is replicated. A custom subresource entry may
		// name the same endpoint slice, and matching it here would report a
		// "<resource>/<verb>" coordinate as replicated.
		return res.Group == gr.Group &&
			res.Name == gr.Resource &&
			!res.IsSubresource() &&
			res.Storage.Virtual != nil &&
			ptr.Deref(res.Storage.Virtual.Reference.APIGroup, "") == cachev1alpha1.SchemeGroupVersion.Group &&
			res.Storage.Virtual.Reference.Kind == clusterCachedResourceEndpointSliceKind
	})
}
