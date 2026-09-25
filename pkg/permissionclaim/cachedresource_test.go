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
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

func cachedStorage(group, kind, name string) apisv1alpha2.ResourceSchemaStorage {
	return apisv1alpha2.ResourceSchemaStorage{
		Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
			Reference: corev1.TypedLocalObjectReference{
				APIGroup: ptr.To(group),
				Kind:     kind,
				Name:     name,
			},
		},
	}
}

func TestServedFromClusterCachedResource(t *testing.T) {
	t.Parallel()

	crdStorage := apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}
	cache := cachev1alpha1.SchemeGroupVersion.Group

	export := func(resources ...apisv1alpha2.ResourceSchema) *apisv1alpha2.APIExport {
		return &apisv1alpha2.APIExport{Spec: apisv1alpha2.APIExportSpec{Resources: resources}}
	}

	sheriffs := schema.GroupResource{Group: "wildwest.dev", Resource: "sheriffs"}

	for _, tc := range []struct {
		name   string
		export *apisv1alpha2.APIExport
		gr     schema.GroupResource
		want   bool
	}{{
		name:   "no export at all",
		export: nil,
		gr:     sheriffs,
	}, {
		name:   "served from CRD storage",
		export: export(apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs", Storage: crdStorage}),
		gr:     sheriffs,
	}, {
		name:   "served from a cached resource",
		export: export(apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs", Storage: cachedStorage(cache, "ClusterCachedResourceEndpointSlice", "sheriffs.wildwest.dev")}),
		gr:     sheriffs,
		want:   true,
	}, {
		name: "another resource on the same export is cached",
		export: export(
			apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "cowboys", Storage: cachedStorage(cache, "ClusterCachedResourceEndpointSlice", "cowboys.wildwest.dev")},
			apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs", Storage: crdStorage},
		),
		gr: sheriffs,
	}, {
		name:   "a different group with the same resource name",
		export: export(apisv1alpha2.ResourceSchema{Group: "other.dev", Name: "sheriffs", Storage: cachedStorage(cache, "ClusterCachedResourceEndpointSlice", "sheriffs.other.dev")}),
		gr:     sheriffs,
	}, {
		// A custom subresource entry may name the same endpoint slice as the
		// resource it hangs off. Reporting it as replicated would drop the claim
		// label from a coordinate whose parent is stored per consumer.
		name:   "a custom subresource entry is not the resource",
		export: export(apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs/deputize", Storage: cachedStorage(cache, "ClusterCachedResourceEndpointSlice", "sheriffs.wildwest.dev")}),
		gr:     schema.GroupResource{Group: "wildwest.dev", Resource: "sheriffs/deputize"},
	}, {
		name:   "virtual storage that is not a cached resource",
		export: export(apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs", Storage: cachedStorage(apisv1alpha2.SchemeGroupVersion.Group, "APIExportEndpointSlice", "sheriffs.wildwest.dev")}),
		gr:     sheriffs,
	}, {
		name:   "the cache group with some other kind",
		export: export(apisv1alpha2.ResourceSchema{Group: "wildwest.dev", Name: "sheriffs", Storage: cachedStorage(cache, "SomethingElse", "sheriffs.wildwest.dev")}),
		gr:     sheriffs,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ServedFromClusterCachedResource(tc.export, tc.gr); got != tc.want {
				t.Fatalf("ServedFromClusterCachedResource() = %v, want %v", got, tc.want)
			}
		})
	}
}
