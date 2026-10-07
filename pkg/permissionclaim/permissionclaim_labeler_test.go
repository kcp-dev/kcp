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

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/apis/v1alpha2/permissionclaims"
)

func TestLabelsForIgnoresDeletingAPIBinding(t *testing.T) {
	t.Parallel()

	secrets := schema.GroupResource{Group: "", Resource: "secrets"}
	claim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: secrets.Group, Resource: secrets.Resource},
		Verbs:         []string{"*"},
	}
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "stealer",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "provider"},
		},
		Spec: apisv1alpha2.APIExportSpec{PermissionClaims: []apisv1alpha2.PermissionClaim{claim}},
	}
	newBinding := func(deleting bool) *apisv1alpha2.APIBinding {
		b := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "stealer",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "consumer"},
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{Export: &apisv1alpha2.ExportBindingReference{Path: "root:provider", Name: "stealer"}},
				PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: claim,
						Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				}},
			},
		}
		if deleting {
			now := metav1.Now()
			b.DeletionTimestamp = &now
			b.Finalizers = []string{"apis.kcp.io/apibinding-finalizer"}
		}
		return b
	}

	expectedKey, expectedValue, err := permissionclaims.ToLabelKeyAndValue(logicalcluster.From(export), export.Name, claim)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		deleting bool
		want     map[string]string
	}{
		"live binding labels the claimed resource": {deleting: false, want: map[string]string{expectedKey: expectedValue}},
		"deleting binding grants nothing anymore":  {deleting: true, want: map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binding := newBinding(tc.deleting)
			l := &Labeler{
				listAPIBindingsAcceptingClaimedGroupResource: func(logicalcluster.Name, schema.GroupResource) ([]*apisv1alpha2.APIBinding, error) {
					return []*apisv1alpha2.APIBinding{binding}, nil
				},
				getAPIBinding:         func(logicalcluster.Name, string) (*apisv1alpha2.APIBinding, error) { return binding, nil },
				getAPIExport:          func(logicalcluster.Path, string) (*apisv1alpha2.APIExport, error) { return export, nil },
				canonicalIdentityHash: func(hash string) string { return hash },
			}

			got, err := l.LabelsFor(t.Context(), logicalcluster.Name("consumer"), secrets, "db-credentials", nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
