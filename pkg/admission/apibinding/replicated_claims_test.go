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

package apibinding

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

// replicatedExport is a provider that serves sheriffs from a cached resource and
// cowboys from its own storage.
func replicatedExport() *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Group: "wildwest.dev",
				Name:  "sheriffs",
				Storage: apisv1alpha2.ResourceSchemaStorage{
					Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
						Reference: corev1.TypedLocalObjectReference{
							APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
							Kind:     "ClusterCachedResourceEndpointSlice",
							Name:     "sheriffs.wildwest.dev",
						},
					},
				},
			}, {
				Group:   "wildwest.dev",
				Name:    "cowboys",
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			}},
		},
	}
}

func claim(resource string, selector apisv1alpha2.PermissionClaimSelector) apisv1alpha2.AcceptablePermissionClaim {
	return apisv1alpha2.AcceptablePermissionClaim{
		State: apisv1alpha2.ClaimAccepted,
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: resource},
				Verbs:         []string{"get", "list", "watch"},
			},
			Selector: selector,
		},
	}
}

var (
	matchAll     = apisv1alpha2.PermissionClaimSelector{MatchAll: true}
	byLabel      = apisv1alpha2.PermissionClaimSelector{LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "sheriff"}}}
	byExpression = apisv1alpha2.PermissionClaimSelector{LabelSelector: metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpExists}},
	}}
)

// A claim on a replicated resource is served without a label requirement, because
// nothing labels a replicated object. That is only sound for a claim that asked
// for every object, so a narrowed one is refused rather than widened.
func TestValidateReplicatedResourceClaims(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		claims     []apisv1alpha2.AcceptablePermissionClaim
		exportErr  error
		exportName string
		wantErr    string
	}{{
		name:   "matchAll on a replicated resource is allowed",
		claims: []apisv1alpha2.AcceptablePermissionClaim{claim("sheriffs", matchAll)},
	}, {
		name:    "matchLabels on a replicated resource is refused",
		claims:  []apisv1alpha2.AcceptablePermissionClaim{claim("sheriffs", byLabel)},
		wantErr: "sheriffs.wildwest.dev is served from a ClusterCachedResource",
	}, {
		name:    "matchExpressions on a replicated resource is refused",
		claims:  []apisv1alpha2.AcceptablePermissionClaim{claim("sheriffs", byExpression)},
		wantErr: "sheriffs.wildwest.dev is served from a ClusterCachedResource",
	}, {
		// The selector is the ordinary way to narrow a claim and must keep working
		// wherever the objects are stored per consumer.
		name:   "matchLabels on a resource with its own storage is allowed",
		claims: []apisv1alpha2.AcceptablePermissionClaim{claim("cowboys", byLabel)},
	}, {
		name: "a narrowed claim is reported even beside an allowed one",
		claims: []apisv1alpha2.AcceptablePermissionClaim{
			claim("cowboys", byLabel),
			claim("sheriffs", byLabel),
		},
		wantErr: "spec.permissionClaims[1].selector",
	}, {
		// An unresolvable export is either unreadable, which the access check
		// reports, or an informer that has not caught up. Neither is an invalid
		// selector.
		name:      "an unresolvable export is left alone",
		claims:    []apisv1alpha2.AcceptablePermissionClaim{claim("sheriffs", byLabel)},
		exportErr: apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), "wildwest"),
	}, {
		name:       "a binding with no export name is left alone",
		claims:     []apisv1alpha2.AcceptablePermissionClaim{claim("sheriffs", byLabel)},
		exportName: "-",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var lookedUpPath logicalcluster.Path
			o := &apiBindingAdmission{
				getAPIExport: func(path logicalcluster.Path, _ string) (*apisv1alpha2.APIExport, error) {
					lookedUpPath = path
					if tc.exportErr != nil {
						return nil, tc.exportErr
					}
					return replicatedExport(), nil
				},
			}

			exportName := "wildwest"
			if tc.exportName == "-" {
				exportName = ""
			}

			ab := &apiBindingV1alpha2{binding: &apisv1alpha2.APIBinding{
				Spec: apisv1alpha2.APIBindingSpec{PermissionClaims: tc.claims},
			}}

			// An empty export path means the export lives in the binding's own
			// workspace, which is the path the lookup has to use.
			err := o.validateReplicatedResourceClaims(ab, logicalcluster.Name("consumer"), "", exportName)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateReplicatedResourceClaims() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateReplicatedResourceClaims() = nil, want an error containing %q", tc.wantErr)
			}
			if got := err.Error(); !strings.Contains(got, tc.wantErr) {
				t.Fatalf("validateReplicatedResourceClaims() = %q, want it to contain %q", got, tc.wantErr)
			}
			if exportName != "" && tc.exportErr == nil && lookedUpPath.String() != "consumer" {
				t.Fatalf("looked up the export at %q, want the binding's own workspace", lookedUpPath)
			}
		})
	}
}
