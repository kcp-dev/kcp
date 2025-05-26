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

package apiexport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

func createAttr(name string, obj runtime.Object, kind, resource string) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		nil,
		apisv1alpha2.Kind(kind).WithVersion("v1alpha2"),
		"",
		name,
		apisv1alpha2.Resource(resource).WithVersion("v1alpha2"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(name string, obj runtime.Object, kind, resource string) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		helpers.ToUnstructuredOrDie(obj),
		apisv1alpha2.Kind(kind).WithVersion("v1alpha2"),
		"",
		name,
		apisv1alpha2.Resource(resource).WithVersion("v1alpha2"),
		"",
		admission.Update,
		&metav1.UpdateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmission(t *testing.T) {
	cases := map[string]struct {
		attr         admission.Attributes
		update       bool
		kind         string
		resource     string
		hasIdentity  bool
		isBuiltIn    bool
		modifyExport func(*apisv1alpha2.APIExport)
		want         error
	}{
		"NotAPIExportKind": {
			kind:      "Something",
			resource:  "apiexports",
			isBuiltIn: false,
		},
		"NotAPIExportResource": {
			kind:        "APIExport",
			resource:    "somethings",
			isBuiltIn:   false,
			hasIdentity: true,
		},
		"ValidCreateBuiltInNoID": {
			kind:      "APIExport",
			resource:  "apiexports",
			isBuiltIn: true,
		},
		"ForbiddenCreateNonBuiltInNoID": {
			kind:      "APIExport",
			resource:  "apiexports",
			isBuiltIn: false,
			want: field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(0).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"),
		},
		"ForbiddenCreateMultipleNonBuiltInNoID": {
			kind:        "APIExport",
			resource:    "apiexports",
			isBuiltIn:   false,
			hasIdentity: true,
			modifyExport: func(ae *apisv1alpha2.APIExport) {
				ae.Spec.PermissionClaims = append(ae.Spec.PermissionClaims, apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "imnot",
						Resource: "builtin",
					},
				})
			},
			want: field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(1).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"),
		},
		"ValidUpdateBuiltInNoID": {
			update:    true,
			kind:      "APIExport",
			resource:  "apiexports",
			isBuiltIn: true,
		},
		"ForbiddenUpdateNonBuiltInNoID": {
			update:    true,
			kind:      "APIExport",
			resource:  "apiexports",
			isBuiltIn: false,
			want: field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(0).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"),
		},
		"ForbiddenUpdateMultipleNonBuiltInNoID": {
			update:      true,
			kind:        "APIExport",
			resource:    "apiexports",
			isBuiltIn:   false,
			hasIdentity: true,
			modifyExport: func(ae *apisv1alpha2.APIExport) {
				ae.Spec.PermissionClaims = append(ae.Spec.PermissionClaims, apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "imnot",
						Resource: "builtin",
					},
				})
			},
			want: field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(1).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"),
		},
		"ForbiddenInvalidResourceSchema": {
			update:      true,
			kind:        "APIExport",
			resource:    "apiexports",
			isBuiltIn:   false,
			hasIdentity: true,
			modifyExport: func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Resources = append(ae.Spec.Resources, apisv1alpha2.ResourceSchema{
					Name:   "foo",
					Group:  "bar",
					Schema: "this.is.invalid",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				})
			},
			want: field.Invalid(
				field.NewPath("spec").
					Child("resources").
					Index(0).
					Child("schema"),
				"this.is.invalid",
				"must end in .foo.bar"),
		},
		"ValidResourceSchemaName": {
			update:      true,
			kind:        "APIExport",
			resource:    "apiexports",
			isBuiltIn:   false,
			hasIdentity: true,
			modifyExport: func(ae *apisv1alpha2.APIExport) {
				ae.Spec.Resources = append(ae.Spec.Resources, apisv1alpha2.ResourceSchema{
					Name:   "wild.wild.west",
					Group:  "sheriffs",
					Schema: "today.wild.wild.west.sheriffs",
				})
			},
		},
		"ValidCreateNonBuiltIn": {
			kind:        "APIExport",
			resource:    "apiexports",
			hasIdentity: true,
			isBuiltIn:   false,
		},
		"ValidUpdateNonBuiltIn": {
			update:      true,
			kind:        "APIExport",
			resource:    "apiexports",
			hasIdentity: true,
			isBuiltIn:   false,
		},
		"ValidNoPermissionClaims": {
			kind:     "APIExport",
			resource: "apiexports",
			modifyExport: func(ae *apisv1alpha2.APIExport) {
				ae.Spec.PermissionClaims = []apisv1alpha2.PermissionClaim{}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ae := &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cool-something",
				},
				Spec: apisv1alpha2.APIExportSpec{
					PermissionClaims: []apisv1alpha2.PermissionClaim{
						{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "some",
								Resource: "somethings",
							},
						},
					},
				},
			}
			if tc.hasIdentity {
				ae.Spec.PermissionClaims[0].IdentityHash = "coolidentityhash"
			}
			if tc.modifyExport != nil {
				tc.modifyExport(ae)
			}
			var attr admission.Attributes
			if tc.update {
				attr = updateAttr("cool-something", ae, tc.kind, tc.resource)
			} else {
				attr = createAttr("cool-something", ae, tc.kind, tc.resource)
			}
			plugin := NewAPIExportAdmission(func(apis.GroupResource) bool {
				return tc.isBuiltIn
			})
			if err := plugin.Validate(context.Background(), attr, nil); err != nil {
				require.Contains(t, err.Error(), tc.want.Error())
				return
			}
			if tc.want != nil {
				t.Errorf("no error returned but expected: %s", tc.want.Error())
			}
		})
	}
}

func TestValidateOverhangingResourceSchemas(t *testing.T) {
	tests := map[string]struct {
		annotations   func() map[string]string
		latestSchemas []string // LatestResourceSchemas
		expectedError string
	}{
		"NoAnnotations": {
			annotations:   func() map[string]string { return nil },
			latestSchemas: nil,
			expectedError: "",
		},
		"EmptyJSON": {
			annotations: func() map[string]string {
				s := apisv1alpha2.ResourceSchema{}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			latestSchemas: nil,
			expectedError: "failed to decode overhanging resource schemas",
		},
		"EmptyLatestSchemaAndAnnotation": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: "[]",
				}
			},
			latestSchemas: []string{},
			expectedError: "",
		},
		"ValidJSON": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.ResourceSchema{{
					Name:   "test",
					Group:  "group",
					Schema: "v1.test.schema",
				}}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			latestSchemas: nil,
			expectedError: "",
		},
		"DuplicateSchemaInAnnotations": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.ResourceSchema{{
					Name:   "test",
					Group:  "group",
					Schema: "v1.test.schema",
				}, {
					Name:   "test",
					Group:  "group",
					Schema: "v1.test.schema",
				}}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			latestSchemas: nil,
			expectedError: "duplicate resource schema",
		},
		"DuplicateSchemaInSpecAndAnnotations": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.ResourceSchema{{
					Name:   "test",
					Group:  "schema",
					Schema: "v1.test.schema",
				}}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			latestSchemas: []string{"v1.test.schema"},
			expectedError: "duplicate resource schema",
		},
		"InvalidJSON": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: "invalid json",
				}
			},
			latestSchemas: nil,
			expectedError: "failed to decode overhanging resource schemas",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ae := &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.annotations(),
				},
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: tc.latestSchemas,
				},
			}
			err := validateOverhangingResourceSchemas(context.TODO(), nil, ae)
			if tc.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}

func TestValidateOverhangingPermissionClaims(t *testing.T) {
	tests := map[string]struct {
		annotations      func() map[string]string
		permissionClaims []apisv1alpha1.PermissionClaim
		expectedError    string
	}{
		"NoAnnotations": {
			annotations:      func() map[string]string { return nil },
			permissionClaims: nil,
			expectedError:    "",
		},
		"EmptyJSON": {
			annotations: func() map[string]string {
				pc := apisv1alpha2.PermissionClaim{}
				data, err := json.Marshal(pc)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: string(data),
				}
			},
			permissionClaims: nil,
			expectedError:    "failed to decode overhanging permission claims",
		},
		"EmptyPermissionClaimsAndAnnotation": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: "[]",
				}
			},
			permissionClaims: []apisv1alpha1.PermissionClaim{},
			expectedError:    "",
		},
		"ValidJSON": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.PermissionClaim{{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "foo",
						Resource: "bar",
					},
					All:          true,
					IdentityHash: "baz",
					Verbs:        []string{"get", "list"},
				}}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			permissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "foo",
						Resource: "bar",
					},
					All:          true,
					IdentityHash: "baz",
				},
			},
			expectedError: "",
		},
		"MismatchInAnnotations": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.PermissionClaim{
					{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    "foo",
							Resource: "bar",
						},
						All:          true,
						IdentityHash: "baz",
						Verbs:        []string{"get", "list"},
					},
					{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    "foo",
							Resource: "baz",
						},
						All:          true,
						IdentityHash: "bar",
						Verbs:        []string{"get"},
					},
				}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: string(data),
				}
			},
			permissionClaims: []apisv1alpha1.PermissionClaim{
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "foo",
						Resource: "bar",
					},
					All:          true,
					IdentityHash: "baz",
				},
				{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "test",
						Resource: "schema",
					},
					All:          true,
					IdentityHash: "random",
				},
			},
			expectedError: "permission claims defined in annotation do not match permission claims defined in spec",
		},
		"InvalidJSON": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: "invalid json",
				}
			},
			permissionClaims: nil,
			expectedError:    "failed to decode overhanging permission claims",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ae := &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.annotations(),
				},
				Spec: apisv1alpha1.APIExportSpec{
					PermissionClaims: tc.permissionClaims,
				},
			}
			err := validateOverhangingPermissionClaims(context.TODO(), nil, ae)
			if tc.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}

func TestValidateVerbsBundle(t *testing.T) {
	path := field.NewPath("spec", "rules", "verbs")

	// Test cases to demonstrate the function's behavior.
	testCases := []struct {
		name        string
		verbs       []string
		expectError bool
		expectedMsg string
	}{
		{
			name:        "valid standalone get",
			verbs:       []string{"get"},
			expectError: false,
		},
		{
			name:        "valid list, get, watch",
			verbs:       []string{"list", "get", "watch"},
			expectError: false,
		},
		{
			name:        "list missing get",
			verbs:       []string{"list", "watch"},
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'get'",
		},
		{
			name:        "list missing watch",
			verbs:       []string{"list", "get"},
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'watch'",
		},
		{
			name:        "list missing get and watch",
			verbs:       []string{"list"},
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'get', 'watch'",
		},
		{
			name:        "watch missing list",
			verbs:       []string{"watch", "get"},
			expectError: true,
			expectedMsg: "if 'watch' verb is present, the following dependent verbs are missing: 'list'",
		},
		{
			name:        "watch missing get",
			verbs:       []string{"watch", "list"},
			expectError: true,
			expectedMsg: "if 'watch' verb is present, the following dependent verbs are missing: 'get'",
		},
		{
			name:        "watch missing list and get",
			verbs:       []string{"watch"},
			expectError: true,
			expectedMsg: "if 'watch' verb is present, the following dependent verbs are missing: 'list', 'get'",
		},
		{
			name:        "patch missing update",
			verbs:       []string{"patch", "create"},
			expectError: true,
			expectedMsg: "if 'patch' verb is present, the following dependent verbs are missing: 'update'",
		},
		{
			name:        "patch missing create",
			verbs:       []string{"patch", "update"},
			expectError: true,
			expectedMsg: "if 'patch' verb is present, the following dependent verbs are missing: 'create'",
		},
		{
			name:        "patch missing update and create",
			verbs:       []string{"patch"},
			expectError: true,
			expectedMsg: "if 'patch' verb is present, the following dependent verbs are missing: 'update', 'create'",
		},
		{
			name:        "valid patch, update, create",
			verbs:       []string{"patch", "update", "create"},
			expectError: false,
		},
		{
			name:        "list and watch both missing get",
			verbs:       []string{"list", "watch"},
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'get'; if 'watch' verb is present, the following dependent verbs are missing: 'get'",
		},
		{
			name:        "empty verbs list",
			verbs:       []string{},
			expectError: false,
		},
		{
			name:        "all verbs valid bundle",
			verbs:       []string{"get", "list", "watch", "create", "update", "delete", "patch"},
			expectError: false,
		},
		{
			name:        "complex scenario: list, patch, missing dependencies",
			verbs:       []string{"list", "patch", "get"}, // list needs watch; patch needs update & create
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'watch'; if 'patch' verb is present, the following dependent verbs are missing: 'update', 'create'",
		},
		{
			name:        "list (missing get, watch) and patch (missing update, create)",
			verbs:       []string{"list", "patch"},
			expectError: true,
			expectedMsg: "if 'list' verb is present, the following dependent verbs are missing: 'get', 'watch'; if 'patch' verb is present, the following dependent verbs are missing: 'update', 'create'",
		},
	}

	for _, tc := range testCases {
		err := validateVerbsBundle(tc.verbs, path)

		if len(tc.expectedMsg) > 0 {
			if err == nil {
				t.Errorf("Expected an error, but got nil.")
			} else if !strings.Contains(err.Error(), tc.expectedMsg) {
				t.Errorf("Expected error message to contain '%s', but got: %s", tc.expectedMsg, err.Error())
			}
		} else if err != nil {
			t.Errorf("Expected no error, but got: %s", err.Error())
		}
	}
}
