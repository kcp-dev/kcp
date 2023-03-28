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
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func createAttr(name string, obj runtime.Object, kind, resource string) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		nil,
		apisv1alpha1.Kind(kind).WithVersion("v1alpha1"),
		"",
		name,
		apisv1alpha1.Resource(resource).WithVersion("v1alpha1"),
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
		apisv1alpha1.Kind(kind).WithVersion("v1alpha1"),
		"",
		name,
		apisv1alpha1.Resource(resource).WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.UpdateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmission(t *testing.T) {
	cases := map[string]struct {
		attr        admission.Attributes
		update      bool
		kind        string
		resource    string
		hasIdentity bool
		isBuiltIn   bool
		modifyPCs   func([]apisv1alpha1.PermissionClaim) []apisv1alpha1.PermissionClaim
		want        error
	}{
		"NotAPIExportKind": {
			kind:      "Something",
			resource:  "apiexports",
			isBuiltIn: false,
		},
		"NotAPIExportResource": {
			kind:      "APIExport",
			resource:  "somethings",
			isBuiltIn: false,
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
			modifyPCs: func(pcs []apisv1alpha1.PermissionClaim) []apisv1alpha1.PermissionClaim {
				pcs = append(pcs, apisv1alpha1.PermissionClaim{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "imnot",
						Resource: "builtin",
					},
				})
				return pcs
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
			modifyPCs: func(pcs []apisv1alpha1.PermissionClaim) []apisv1alpha1.PermissionClaim {
				pcs = append(pcs, apisv1alpha1.PermissionClaim{
					GroupResource: apisv1alpha1.GroupResource{
						Group:    "imnot",
						Resource: "builtin",
					},
				})
				return pcs
			},
			want: field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(1).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"),
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
			modifyPCs: func(pc []apisv1alpha1.PermissionClaim) []apisv1alpha1.PermissionClaim {
				return []apisv1alpha1.PermissionClaim{}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ae := &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cool-something",
				},
				Spec: apisv1alpha1.APIExportSpec{
					PermissionClaims: []apisv1alpha1.PermissionClaim{
						{
							GroupResource: apisv1alpha1.GroupResource{
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
			if tc.modifyPCs != nil {
				ae.Spec.PermissionClaims = tc.modifyPCs(ae.Spec.PermissionClaims)
			}
			var attr admission.Attributes
			if tc.update {
				attr = updateAttr("cool-something", ae, tc.kind, tc.resource)
			} else {
				attr = createAttr("cool-something", ae, tc.kind, tc.resource)
			}
			plugin := NewAPIExportAdmission(func(apisv1alpha1.GroupResource) bool {
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
