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

package conversion

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestSchemaBasedConverterPermissionClaimsRoundTrip(t *testing.T) {
	t.Parallel()

	f, err := NewCRConverterFactory(nil, wait.ForeverTestTimeout, false)
	require.NoError(t, err)

	converter, err := f.NewConverter(&apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{Group: apis.GroupName},
	})
	require.NoError(t, err)

	v2 := &apisv1alpha2.APIBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIBinding",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{
						GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"},
						Verbs:         []string{"*"},
					},
					Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimAccepted,
			}},
		},
	}

	uns, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v2)
	require.NoError(t, err)

	out, err := converter.Convert(&unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{{Object: uns}},
	}, apisv1alpha1.SchemeGroupVersion)
	require.NoError(t, err)
	require.Len(t, out.Items, 1)

	var v1 apisv1alpha1.APIBinding
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(out.Items[0].Object, &v1))
	require.Len(t, v1.Spec.PermissionClaims, 1)
	require.Equal(t, apisv1alpha1.ClaimAccepted, v1.Spec.PermissionClaims[0].State)
	require.Equal(t, "configmaps", v1.Spec.PermissionClaims[0].Resource)
	require.True(t, v1.Spec.PermissionClaims[0].All)
}
