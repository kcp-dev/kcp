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

package apiexport

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func virtualStorage() apisv1alpha2.ResourceSchemaStorage {
	return apisv1alpha2.ResourceSchemaStorage{
		Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
			Reference: corev1.TypedLocalObjectReference{
				APIGroup: ptr.To("compute.example.com"),
				Kind:     "VirtualMachineEndpointSlice",
				Name:     "compute",
			},
		},
	}
}

// TestValidateCustomSubresourceEntry covers the rules admission enforces on an
// entry named "<resource>/<subresource>". The CRD schema cannot express them,
// because they depend on other entries and on names the object's shape owns.
func TestValidateCustomSubresourceEntry(t *testing.T) {
	t.Parallel()

	// "cowboys" is exported as an ordinary CRD-backed resource in every case.
	exported := sets.New("wildwest.dev/cowboys")

	entry := func(name string) apisv1alpha2.ResourceSchema {
		_, sub := apisv1alpha2.ResourceSchema{Name: name}.SplitName()
		return apisv1alpha2.ResourceSchema{
			Group:   "wildwest.dev",
			Name:    name,
			Schema:  "today." + sub + ".wildwest.dev",
			Storage: virtualStorage(),
		}
	}

	for name, tc := range map[string]struct {
		schema  apisv1alpha2.ResourceSchema
		wantErr string
	}{
		"a custom subresource is accepted": {
			schema: entry("cowboys/ssh"),
		},
		"status may not be redeclared as a custom subresource": {
			schema:  entry("cowboys/status"),
			wantErr: "declared on the APIResourceSchema",
		},
		"scale may not be redeclared as a custom subresource": {
			schema:  entry("cowboys/scale"),
			wantErr: "declared on the APIResourceSchema",
		},
		"a subresource of a resource this export does not offer is rejected": {
			schema:  entry("sheriffs/ssh"),
			wantErr: `resource "sheriffs" is not exported`,
		},
		"a subresource may not use CRD storage": {
			schema: func() apisv1alpha2.ResourceSchema {
				s := entry("cowboys/ssh")
				s.Storage = apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}}
				return s
			}(),
			wantErr: "must use virtual storage",
		},
		"the endpoint slice reference is required": {
			schema: func() apisv1alpha2.ResourceSchema {
				s := entry("cowboys/ssh")
				s.Storage.Virtual.Reference.Name = ""
				return s
			}(),
			wantErr: "virtual workspace URL",
		},
		"the schema must be named after the subresource, not the resource": {
			schema: func() apisv1alpha2.ResourceSchema {
				s := entry("cowboys/ssh")
				s.Schema = "today.cowboys.wildwest.dev"
				return s
			}(),
			wantErr: "must end in .ssh.wildwest.dev",
		},
		"an ordinary resource entry is unaffected": {
			schema: apisv1alpha2.ResourceSchema{
				Group:   "wildwest.dev",
				Name:    "cowboys",
				Schema:  "today.cowboys.wildwest.dev",
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := validateResourceSchema(tc.schema, exported, field.NewPath("spec", "resources").Index(0))

			if tc.wantErr == "" {
				require.Nil(t, err)
				return
			}

			require.NotNil(t, err, "expected the entry to be rejected")
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidateSubresourceClaims covers the rule that a claim on a custom
// subresource needs a claim on the resource that serves it. status is exempt.
func TestValidateSubresourceClaims(t *testing.T) {
	t.Parallel()

	claim := func(resource string) apisv1alpha2.PermissionClaim {
		return apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: resource},
			Verbs:         []string{"create"},
		}
	}

	for _, tc := range []struct {
		name     string
		claims   []apisv1alpha2.PermissionClaim
		exported sets.Set[string]
		wantErr  string
	}{
		{
			name:   "subresource claimed with its parent",
			claims: []apisv1alpha2.PermissionClaim{claim("cowboys"), claim("cowboys/shoot")},
		},
		{
			name:     "subresource claimed on a resource this export serves itself",
			claims:   []apisv1alpha2.PermissionClaim{claim("cowboys/shoot")},
			exported: sets.New("wildwest.dev/cowboys"),
		},
		{
			name:    "subresource claimed alone",
			claims:  []apisv1alpha2.PermissionClaim{claim("cowboys/shoot")},
			wantErr: `requires a claim on "cowboys"`,
		},
		{
			name:   "status needs no parent claim",
			claims: []apisv1alpha2.PermissionClaim{claim("cowboys/status")},
		},
		{
			name:    "parent claimed in another group does not count",
			claims:  []apisv1alpha2.PermissionClaim{{GroupResource: apisv1alpha2.GroupResource{Group: "other.dev", Resource: "cowboys"}}, claim("cowboys/shoot")},
			wantErr: `requires a claim on "cowboys"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exported := tc.exported
			if exported == nil {
				exported = sets.New[string]()
			}

			err := validateSubresourceClaims(&apisv1alpha2.APIExport{
				Spec: apisv1alpha2.APIExportSpec{PermissionClaims: tc.claims},
			}, exported)

			if tc.wantErr == "" {
				require.Nil(t, err)
				return
			}
			require.NotNil(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
