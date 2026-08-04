/*
Copyright 2025 The kcp Authors.

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

package v1alpha2

import "testing"

func TestVirtualStorageIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		resource ResourceSchema
		want     string
	}{
		"virtual storage is named after the resource": {
			resource: ResourceSchema{
				Name:    "bucketinfos",
				Group:   "s3.example.com",
				Storage: ResourceSchemaStorage{Virtual: &ResourceSchemaStorageVirtual{}},
			},
			want: "bucketinfos.s3.example.com",
		},
		"the core group leaves a trailing dot rather than reading as a bare name": {
			resource: ResourceSchema{
				Name:    "widgets",
				Group:   "",
				Storage: ResourceSchemaStorage{Virtual: &ResourceSchemaStorageVirtual{}},
			},
			want: "widgets.",
		},
		// The identity is what tells a bound CRD apart from one backed by
		// ordinary storage, so it has to stay empty when there is none.
		"crd storage has no identity": {
			resource: ResourceSchema{
				Name:    "buckets",
				Group:   "s3.example.com",
				Storage: ResourceSchemaStorage{CRD: &ResourceSchemaStorageCRD{}},
			},
			want: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := tt.resource.VirtualStorageIdentity(); got != tt.want {
				t.Errorf("VirtualStorageIdentity() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The identity is an index key, and two resources sharing one would make a
// bound CRD ambiguous. Name and group are what admission already keeps unique
// within an APIExport, which is why deriving from them is safe.
func TestVirtualStorageIdentityIsUniqueWithinAnExport(t *testing.T) {
	t.Parallel()

	export := APIExport{
		Spec: APIExportSpec{
			Resources: []ResourceSchema{
				{Name: "bucketinfos", Group: "s3.example.com", Storage: ResourceSchemaStorage{Virtual: &ResourceSchemaStorageVirtual{}}},
				{Name: "bucketinfos", Group: "gcs.example.com", Storage: ResourceSchemaStorage{Virtual: &ResourceSchemaStorageVirtual{}}},
				{Name: "buckets", Group: "s3.example.com", Storage: ResourceSchemaStorage{Virtual: &ResourceSchemaStorageVirtual{}}},
			},
		},
	}

	seen := map[string]bool{}
	for _, resource := range export.Spec.Resources {
		identity := resource.VirtualStorageIdentity()
		if seen[identity] {
			t.Errorf("identity %q is used by more than one resource", identity)
		}
		seen[identity] = true
	}
}
