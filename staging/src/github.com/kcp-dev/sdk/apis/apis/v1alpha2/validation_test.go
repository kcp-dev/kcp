/*
Copyright 2025 The KCP Authors.

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
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidateAPIBindingPermissionClaims(t *testing.T) {
	tests := map[string]struct {
		permissionClaims []AcceptablePermissionClaim
		wantErrs         []string
	}{
		"matchAll": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							MatchAll: true,
						},
					},
				},
			},
			wantErrs: nil,
		},
		"matchLabels": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"test": "test",
								},
							},
						},
					},
				},
			},
			wantErrs: nil,
		},
		"matchExpressions": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      "test",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"test"},
									},
								},
							},
						},
					},
				},
			},
			wantErrs: nil,
		},
		"none": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{},
					},
				},
			},
			wantErrs: []string{"spec.permissionClaims[0].selector: Required value: either one of matchAll, matchLabels, or matchExpressions must be set"},
		},
		"empty": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							MatchAll: false,
							LabelSelector: metav1.LabelSelector{
								MatchLabels:      map[string]string{},
								MatchExpressions: []metav1.LabelSelectorRequirement{},
							},
						},
					},
				},
			},
			wantErrs: []string{"spec.permissionClaims[0].selector: Required value: either one of matchAll, matchLabels, or matchExpressions must be set"},
		},
		"matchAll+matchLabels+matchExpressions": {
			permissionClaims: []AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							MatchAll: true,
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"test": "test",
								},
							},
						},
					},
				},
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							MatchAll: true,
							LabelSelector: metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      "test",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"test"},
									},
								},
							},
						},
					},
				},
				{
					ScopedPermissionClaim: ScopedPermissionClaim{
						Selector: PermissionClaimSelector{
							MatchAll: true,
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"test": "test",
								},
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      "test",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"test"},
									},
								},
							},
						},
					},
				},
			},
			wantErrs: []string{
				"spec.permissionClaims[0].selector.matchLabels: Invalid value: {\"matchLabels\":{\"test\":\"test\"},\"matchAll\":true}: matchLabels cannot be used with matchAll",
				"spec.permissionClaims[1].selector.matchExpressions: Invalid value: {\"matchExpressions\":[{\"key\":\"test\",\"operator\":\"In\",\"values\":[\"test\"]}],\"matchAll\":true}: matchExpressions cannot be used with matchAll",
				"spec.permissionClaims[2].selector.matchExpressions: Invalid value: {\"matchLabels\":{\"test\":\"test\"},\"matchExpressions\":[{\"key\":\"test\",\"operator\":\"In\",\"values\":[\"test\"]}],\"matchAll\":true}: matchExpressions cannot be used with matchAll",
				"spec.permissionClaims[2].selector.matchLabels: Invalid value: {\"matchLabels\":{\"test\":\"test\"},\"matchExpressions\":[{\"key\":\"test\",\"operator\":\"In\",\"values\":[\"test\"]}],\"matchAll\":true}: matchLabels cannot be used with matchAll",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := ValidateAPIBindingPermissionClaims(tc.permissionClaims, field.NewPath("spec", "permissionClaims"))

			// Convert FieldErrors into a string slice
			errs := []string{}
			for _, err := range got {
				errs = append(errs, err.Error())
			}

			slices.Sort(errs)
			slices.Sort(tc.wantErrs)

			if !equality.Semantic.DeepEqual(errs, tc.wantErrs) {
				t.Errorf("ValidateAPIBindingPermissionClaims() = %v, want %v", errs, tc.wantErrs)
			}
		})
	}
}
