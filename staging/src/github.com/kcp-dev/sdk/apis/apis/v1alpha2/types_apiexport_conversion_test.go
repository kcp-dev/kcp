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

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apitest "github.com/kcp-dev/sdk/apis/test"
)

func TestConvertV1Alpha2APIExports(t *testing.T) {
	t.Parallel()
	testcases := []APIExport{
		{
			Spec: APIExportSpec{},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}, {
					Group:  "bar",
					Name:   "baz",
					Schema: "v1.baz.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
				PermissionClaims: []PermissionClaim{{
					GroupResource: GroupResource{
						Resource: "configmaps",
					},
					Verbs: []string{"get"},
				}},
			},
		},
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						Virtual: &ResourceSchemaStorageVirtual{
							Reference: corev1.TypedLocalObjectReference{
								APIGroup: ptr.To("example.com"),
								Kind:     "MyEndpointSlice",
								Name:     "my-virtual-resource",
							},
						},
					},
				}},
				PermissionClaims: []PermissionClaim{{
					GroupResource: GroupResource{
						Resource: "configmaps",
					},
					Verbs: []string{"get"},
				}},
			},
		},
		// Test case with DefaultSelector on PermissionClaim
		{
			Spec: APIExportSpec{
				Resources: []ResourceSchema{{
					Group:  "bar",
					Name:   "foo",
					Schema: "v1.foo.bar",
					Storage: ResourceSchemaStorage{
						CRD: &ResourceSchemaStorageCRD{},
					},
				}},
				PermissionClaims: []PermissionClaim{{
					GroupResource: GroupResource{
						Resource: "configmaps",
					},
					Verbs: []string{"get", "list"},
					DefaultSelector: &PermissionClaimSelector{
						LabelSelector: metav1.LabelSelector{
							MatchLabels: map[string]string{
								"platform-mesh.io/enabled": "true",
							},
						},
					},
				}},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	for _, testcase := range testcases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			v1, err := scheme.ConvertToVersion(&testcase, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}

			v2, err := scheme.ConvertToVersion(v1, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}

			// Drop annotation we know this object will have from v1alpha1 -> v1alpha2 conversion.
			v2.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())
			v2obj := v2.(*APIExport)
			delete(v2obj.Annotations, PermissionClaimsV1Alpha1Annotation)
			if len(v2obj.Annotations) == 0 {
				v2obj.Annotations = nil
			}

			if changes := cmp.Diff(&testcase, v2obj); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}

func TestConvertV1Alpha1APIExports(t *testing.T) {
	t.Parallel()
	testcases := []apisv1alpha1.APIExport{
		{
			Spec: apisv1alpha1.APIExportSpec{},
		},
		{
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{
					"v1.foo.org",
					"v1.bar.org",
				},
			},
		},
		{
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{
					"v1.foo.org",
					"v1.bar.org",
				},
				PermissionClaims: []apisv1alpha1.PermissionClaim{{
					GroupResource: apisv1alpha1.GroupResource{
						Resource: "configmaps",
					},
					All: true,
				}},
			},
		},
		// Test case with a ResourceSelector, which has no equivalent in v1alpha2 and has to survive
		// the round trip next to a claim that uses All.
		{
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{
					"v1.foo.org",
				},
				PermissionClaims: []apisv1alpha1.PermissionClaim{{
					GroupResource: apisv1alpha1.GroupResource{
						Resource: "configmaps",
					},
					ResourceSelector: []apisv1alpha1.ResourceSelector{{
						Name:      "my-config",
						Namespace: "default",
					}},
				}, {
					GroupResource: apisv1alpha1.GroupResource{
						Resource: "namespaces",
					},
					All: true,
				}},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	for _, testcase := range testcases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			v2, err := scheme.ConvertToVersion(&testcase, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}

			v1, err := scheme.ConvertToVersion(v2, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}

			v1.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())

			if changes := cmp.Diff(&testcase, v1); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}

// TestConvertV1Alpha2APIExportsToValidV1Alpha1PermissionClaims makes sure that every PermissionClaim of an APIExport
// that is read as v1alpha1 satisfies the v1alpha1 validation rule ("exactly one of all and resourceSelector"), even if
// the APIExport never was a v1alpha1 object and hence has no annotation that retains All and ResourceSelector.
func TestConvertV1Alpha2APIExportsToValidV1Alpha1PermissionClaims(t *testing.T) {
	t.Parallel()

	// This is the v1alpha1 view of what has been stored in the v1alpha1 annotation by a previous
	// v1alpha1 -> v1alpha2 conversion.
	annotatedClaims, err := json.Marshal([]apisv1alpha1.PermissionClaim{{
		GroupResource: apisv1alpha1.GroupResource{
			Resource: "configmaps",
		},
		ResourceSelector: []apisv1alpha1.ResourceSelector{{
			Name:      "my-config",
			Namespace: "default",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	testcases := []struct {
		name        string
		annotations map[string]string
		claims      []PermissionClaim
		want        []apisv1alpha1.PermissionClaim
	}{
		{
			name: "claims without annotation default to all",
			claims: []PermissionClaim{
				{GroupResource: GroupResource{Resource: "namespaces"}, Verbs: []string{"get"}},
				{GroupResource: GroupResource{Resource: "configmaps"}, Verbs: []string{"*"}},
				{GroupResource: GroupResource{Group: "example.com", Resource: "widgets"}, IdentityHash: "abc", Verbs: []string{"get", "list"}},
			},
			want: []apisv1alpha1.PermissionClaim{
				{GroupResource: apisv1alpha1.GroupResource{Resource: "namespaces"}, All: true},
				{GroupResource: apisv1alpha1.GroupResource{Resource: "configmaps"}, All: true},
				{GroupResource: apisv1alpha1.GroupResource{Group: "example.com", Resource: "widgets"}, IdentityHash: "abc", All: true},
			},
		},
		{
			// v1alpha1 can only select by name and namespace, so a label selector cannot be expressed.
			// The claim itself is not narrowed by a defaultSelector, so it is a claim on all objects.
			name: "defaultSelector does not narrow the claim",
			claims: []PermissionClaim{
				{
					GroupResource: GroupResource{Resource: "namespaces"},
					Verbs:         []string{"get"},
					DefaultSelector: &PermissionClaimSelector{
						LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"foo": "bar"}},
					},
				},
				{
					GroupResource:   GroupResource{Resource: "configmaps"},
					Verbs:           []string{"*"},
					DefaultSelector: &PermissionClaimSelector{MatchAll: true},
				},
			},
			want: []apisv1alpha1.PermissionClaim{
				{GroupResource: apisv1alpha1.GroupResource{Resource: "namespaces"}, All: true},
				{GroupResource: apisv1alpha1.GroupResource{Resource: "configmaps"}, All: true},
			},
		},
		{
			// A claim that was added to a v1alpha2 object by hand next to a claim that was created via v1alpha1.
			name:        "claim missing from annotation defaults to all and annotated claim is retained",
			annotations: map[string]string{PermissionClaimsV1Alpha1Annotation: string(annotatedClaims)},
			claims: []PermissionClaim{
				{GroupResource: GroupResource{Resource: "configmaps"}, Verbs: []string{"get"}},
				{GroupResource: GroupResource{Resource: "namespaces"}, Verbs: []string{"get"}},
			},
			want: []apisv1alpha1.PermissionClaim{
				{
					GroupResource:    apisv1alpha1.GroupResource{Resource: "configmaps"},
					ResourceSelector: []apisv1alpha1.ResourceSelector{{Name: "my-config", Namespace: "default"}},
				},
				{GroupResource: apisv1alpha1.GroupResource{Resource: "namespaces"}, All: true},
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	// Validates claims against the CEL rule of the generated v1alpha1 CRD, which is what rejects the
	// object when it is written back to kcp.
	pth := "openAPIV3Schema.properties.spec.properties.permissionClaims.items"
	validator, found := apitest.FieldValidatorsFromFile(t, "../../../../../../../../config/crds/apis.kcp.io_apiexports.yaml")["v1alpha1"][pth]
	require.True(t, found, "failed to find validator for %s", pth)

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			t.Parallel()

			export := &APIExport{
				ObjectMeta: metav1.ObjectMeta{Name: "example.kcp.io", Annotations: testcase.annotations},
				Spec:       APIExportSpec{PermissionClaims: testcase.claims},
			}

			v1, err := scheme.ConvertToVersion(export, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}
			v1export := v1.(*apisv1alpha1.APIExport)

			for _, claim := range v1export.Spec.PermissionClaims {
				encoded, err := json.Marshal(claim)
				if err != nil {
					t.Fatal(err)
				}
				var obj map[string]any
				if err := json.Unmarshal(encoded, &obj); err != nil {
					t.Fatal(err)
				}
				if errs := validator(obj, nil); len(errs) > 0 {
					t.Errorf("claim %q is not valid for v1alpha1: %v", claim.String(), errs)
				}
			}
			if changes := cmp.Diff(testcase.want, v1export.Spec.PermissionClaims); changes != "" {
				t.Fatalf("unexpected v1alpha1 claims:\n%s", changes)
			}

			// Writing the v1alpha1 object back unchanged (as the api-syncagent does) must keep the claims
			// stable when the object is read as v1alpha1 again.
			v2, err := scheme.ConvertToVersion(v1, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}
			v1again, err := scheme.ConvertToVersion(v2, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1 for a second time: %v", err)
			}
			if changes := cmp.Diff(testcase.want, v1again.(*apisv1alpha1.APIExport).Spec.PermissionClaims); changes != "" {
				t.Fatalf("unexpected v1alpha1 claims after writing the object back:\n%s", changes)
			}
		})
	}
}
