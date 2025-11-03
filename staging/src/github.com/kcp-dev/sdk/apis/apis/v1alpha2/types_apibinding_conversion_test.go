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
	"testing"

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

func TestConvertV1Alpha1APIBinding(t *testing.T) {
	testcases := []apisv1alpha1.APIBinding{
		{
			Spec: apisv1alpha1.APIBindingSpec{},
		},
		{
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
			},
		},
		{
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
				PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Resource: "configmaps",
						},
						All: true,
					},
					State: apisv1alpha1.ClaimAccepted,
				}},
			},
		},
		{
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
				PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Resource: "configmaps",
						},
						ResourceSelector: []apisv1alpha1.ResourceSelector{
							{
								Name:      "test",
								Namespace: "default",
							},
						},
					},
					State: apisv1alpha1.ClaimAccepted,
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
			v2, err := scheme.ConvertToVersion(&testcase, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 to v1alpha2: %v", err)
			}

			v1, err := scheme.ConvertToVersion(v2, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 back to v1alpha1: %v", err)
			}

			v1.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())

			if changes := cmp.Diff(&testcase, v1); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}

func TestConvertV1Alpha2APIBindings(t *testing.T) {
	testcases := []APIBinding{
		{
			Spec: APIBindingSpec{},
		},
		{
			Spec: APIBindingSpec{
				Reference: BindingReference{
					Export: &ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
			},
		},
		{
			Spec: APIBindingSpec{
				Reference: BindingReference{
					Export: &ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
				PermissionClaims: []AcceptablePermissionClaim{{
					ScopedPermissionClaim: ScopedPermissionClaim{
						PermissionClaim: PermissionClaim{
							GroupResource: GroupResource{
								Resource: "configmaps",
							},
							Verbs: []string{"get"},
						},
						Selector: PermissionClaimSelector{
							MatchAll: true,
						},
					},
					State: ClaimAccepted,
				}},
			},
		},
		{
			Spec: APIBindingSpec{
				Reference: BindingReference{
					Export: &ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
				PermissionClaims: []AcceptablePermissionClaim{{
					ScopedPermissionClaim: ScopedPermissionClaim{
						PermissionClaim: PermissionClaim{
							GroupResource: GroupResource{
								Resource: "configmaps",
							},
							Verbs: []string{"get"},
						},
						Selector: PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"test": "label",
								},
							},
						},
					},
					State: ClaimAccepted,
				}},
			},
		},
		{
			Spec: APIBindingSpec{
				Reference: BindingReference{
					Export: &ExportBindingReference{
						Path: "foo",
						Name: "bar",
					},
				},
				PermissionClaims: []AcceptablePermissionClaim{{
					ScopedPermissionClaim: ScopedPermissionClaim{
						PermissionClaim: PermissionClaim{
							GroupResource: GroupResource{
								Resource: "configmaps",
							},
							Verbs: []string{"get"},
						},
						Selector: PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									{
										Key:      "test",
										Operator: metav1.LabelSelectorOpIn,
										Values:   []string{"expression"},
									},
								},
							},
						},
					},
					State: ClaimAccepted,
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
			v1, err := scheme.ConvertToVersion(&testcase, apisv1alpha1.SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha2 to v1alpha1: %v", err)
			}

			v2, err := scheme.ConvertToVersion(v1, SchemeGroupVersion)
			if err != nil {
				t.Fatalf("Failed to convert v1alpha1 back to v1alpha2: %v", err)
			}
			v2obj := v2.(*APIBinding)
			delete(v2obj.Annotations, PermissionClaimsV1Alpha1Annotation)
			if len(v2obj.Annotations) == 0 {
				v2obj.Annotations = nil
			}

			v2.GetObjectKind().SetGroupVersionKind(testcase.GroupVersionKind())

			if changes := cmp.Diff(&testcase, v2); changes != "" {
				t.Fatalf("unexpected diff:\n%s", changes)
			}
		})
	}
}
