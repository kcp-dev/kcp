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

func TestValidateOverhangingAnnotations(t *testing.T) {
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
			err := validateOverhangingAnnotations(context.TODO(), nil, ae)
			if tc.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}
