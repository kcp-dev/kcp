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

package fuzzer

import (
	"strings"

	fuzz "github.com/google/gofuzz"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"

	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

// Funcs returns the fuzzer functions for the apiserverinternal api group.
func Funcs(codecs runtimeserializer.CodecFactory) []any {
	return []any{
		func(r *metav1.ManagedFieldsEntry, c fuzz.Continue) {
			// match the fuzzer default content for runtime.Object
			r.APIVersion = "v1"
		},
		func(r *v1alpha2.APIExport, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIExport, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIExportSpec, c fuzz.Continue) {
			c.FuzzNoCustom(r)

			r.LatestResourceSchemas = []string{
				nonEmptyString(c.RandString) + "." + nonEmptyString(c.RandString) + "." + nonEmptyString(c.RandString),
			}
		},
		func(r *v1alpha2.APIExportSpec, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.Resources = nil
			for range c.Intn(5) {
				name := nonEmptyString(c.RandString)
				group := nonEmptyString(c.RandString)
				schema := nonEmptyString(c.RandString) + "." + name + "." + group
				r.Resources = append(r.Resources, v1alpha2.ResourceSchema{
					Group:  group,
					Name:   name,
					Schema: schema,
					Storage: v1alpha2.ResourceSchemaStorage{
						CRD: &v1alpha2.ResourceSchemaStorageCRD{},
					},
				})
			}
			r.PermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)
				verbs := []string{}
				numVerbs := c.Intn(5) + 1 // the lower bound is 0, but 0 verbs is not a valid combination
				for range numVerbs {
					verbs = append(verbs, nonEmptyString(c.RandString))
				}
				r.PermissionClaims = append(r.PermissionClaims, v1alpha2.PermissionClaim{
					GroupResource: v1alpha2.GroupResource{
						Group:    group,
						Resource: resource,
					},
					IdentityHash: identityHash,
					Verbs:        verbs,
				})
			}
		},
		func(r *v1alpha2.APIBinding, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIBinding, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIBindingSpec, c fuzz.Continue) {
			c.FuzzNoCustom(r)

			r.PermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)

				apc := v1alpha1.AcceptablePermissionClaim{
					PermissionClaim: v1alpha1.PermissionClaim{
						GroupResource: v1alpha1.GroupResource{
							Group:    group,
							Resource: resource,
						},
						IdentityHash: identityHash,
						All:          c.RandBool(),
					},
				}

				if !apc.All {
					apc.ResourceSelector = []v1alpha1.ResourceSelector{}
					for range c.Intn(5) + 1 {
						apc.ResourceSelector = append(apc.ResourceSelector, v1alpha1.ResourceSelector{Name: nonEmptyString(c.RandString), Namespace: nonEmptyString(c.RandString)})
					}
				}

				r.PermissionClaims = append(r.PermissionClaims, apc)
			}
		},
		func(r *v1alpha1.APIBindingStatus, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.AppliedPermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)

				pc := v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    group,
						Resource: resource,
					},
					IdentityHash: identityHash,
					All:          c.RandBool(),
				}

				if !pc.All {
					pc.ResourceSelector = []v1alpha1.ResourceSelector{}
					for range c.Intn(5) + 1 {
						pc.ResourceSelector = append(pc.ResourceSelector, v1alpha1.ResourceSelector{Name: nonEmptyString(c.RandString), Namespace: nonEmptyString(c.RandString)})
					}
				}

				r.AppliedPermissionClaims = append(r.AppliedPermissionClaims, pc)
			}
			r.ExportPermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)

				pc := v1alpha1.PermissionClaim{
					GroupResource: v1alpha1.GroupResource{
						Group:    group,
						Resource: resource,
					},
					IdentityHash: identityHash,
					All:          c.RandBool(),
				}

				if !pc.All {
					pc.ResourceSelector = []v1alpha1.ResourceSelector{}
					for range c.Intn(5) + 1 {
						pc.ResourceSelector = append(pc.ResourceSelector, v1alpha1.ResourceSelector{Name: nonEmptyString(c.RandString), Namespace: nonEmptyString(c.RandString)})
					}
				}
				r.ExportPermissionClaims = append(r.ExportPermissionClaims, pc)
			}
		},
		func(r *v1alpha2.APIBindingSpec, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.PermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)
				verbs := []string{}
				numVerbs := c.Intn(5) + 1 // the lower bound is 0, but 0 verbs is not a valid combination
				for range numVerbs {
					verbs = append(verbs, nonEmptyString(c.RandString))
				}

				selector := v1alpha2.PermissionClaimSelector{}

				switch c.Intn(3) {
				case 0:
					selector.MatchAll = true
				case 1:
					labels := make(map[string]string)
					numLabels := c.Intn(5) + 1
					for range numLabels {
						labels[nonEmptyString(c.RandString)] = nonEmptyString(c.RandString)
					}
					selector.MatchLabels = labels
				default:
					numExpressions := c.Intn(5) + 1
					expressions := make([]metav1.LabelSelectorRequirement, numExpressions)
					for range numExpressions {
						expressions = append(expressions, metav1.LabelSelectorRequirement{
							Key:      nonEmptyString(c.RandString),
							Operator: metav1.LabelSelectorOpIn,
							Values: []string{
								nonEmptyString(c.RandString),
							},
						})
					}
					selector.MatchExpressions = expressions
				}

				r.PermissionClaims = append(r.PermissionClaims, v1alpha2.AcceptablePermissionClaim{
					ScopedPermissionClaim: v1alpha2.ScopedPermissionClaim{
						PermissionClaim: v1alpha2.PermissionClaim{
							GroupResource: v1alpha2.GroupResource{
								Group:    group,
								Resource: resource,
							},
							IdentityHash: identityHash,
							Verbs:        verbs,
						},
						Selector: selector,
					},
					State: v1alpha2.ClaimAccepted,
				})
			}
		},
		func(r *v1alpha2.APIBindingStatus, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.AppliedPermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)
				verbs := []string{}
				numVerbs := c.Intn(5) + 1 // the lower bound is 0, but 0 verbs is not a valid combination
				for range numVerbs {
					verbs = append(verbs, nonEmptyString(c.RandString))
				}

				selector := v1alpha2.PermissionClaimSelector{}

				switch c.Intn(3) {
				case 0:
					selector.MatchAll = true
				case 1:
					labels := make(map[string]string)
					numLabels := c.Intn(5) + 1
					for range numLabels {
						labels[nonEmptyString(c.RandString)] = nonEmptyString(c.RandString)
					}
					selector.MatchLabels = labels
				default:
					numExpressions := c.Intn(5) + 1
					expressions := make([]metav1.LabelSelectorRequirement, numExpressions)
					for range numExpressions {
						expressions = append(expressions, metav1.LabelSelectorRequirement{
							Key:      nonEmptyString(c.RandString),
							Operator: metav1.LabelSelectorOpIn,
							Values: []string{
								nonEmptyString(c.RandString),
							},
						})
					}
					selector.MatchExpressions = expressions
				}

				r.AppliedPermissionClaims = append(r.AppliedPermissionClaims, v1alpha2.ScopedPermissionClaim{
					PermissionClaim: v1alpha2.PermissionClaim{
						GroupResource: v1alpha2.GroupResource{
							Group:    group,
							Resource: resource,
						},
						IdentityHash: identityHash,
						Verbs:        verbs,
					},
					Selector: selector,
				})
			}
			r.ExportPermissionClaims = nil
			for range c.Intn(5) {
				group := nonEmptyString(c.RandString)
				resource := nonEmptyString(c.RandString)
				identityHash := nonEmptyString(c.RandString)
				verbs := []string{}
				numVerbs := c.Intn(5) + 1 // the lower bound is 0, but 0 verbs is not a valid combination
				for range numVerbs {
					verbs = append(verbs, nonEmptyString(c.RandString))
				}
				r.ExportPermissionClaims = append(r.ExportPermissionClaims, v1alpha2.PermissionClaim{
					GroupResource: v1alpha2.GroupResource{
						Group:    group,
						Resource: resource,
					},
					IdentityHash: identityHash,
					Verbs:        verbs,
				})
			}
		},
		func(r *v1alpha1.Identity, c fuzz.Continue) {
			c.FuzzNoCustom(r)

			r.SecretRef = &corev1.SecretReference{}
			c.Fuzz(r.SecretRef)
		},
		func(r *v1alpha2.APIExportList, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIExportList, c fuzz.Continue) {
			c.FuzzNoCustom(r)
			r.TypeMeta = metav1.TypeMeta{}
			r.Kind = ""
			r.APIVersion = ""
		},
		func(r *v1alpha1.APIResourceSchemaSpec, c fuzz.Continue) {
			r.Conversion = &v1alpha1.CustomResourceConversion{}

			none := v1alpha1.ConversionStrategyType("None")
			webhook := v1alpha1.ConversionStrategyType("Webhook")

			values := []v1alpha1.ConversionStrategyType{none, webhook}
			r.Conversion.Strategy = values[c.Intn(len(values))]

			if r.Conversion.Strategy == webhook {
				r.Conversion.Webhook = &v1alpha1.WebhookConversion{}
				c.Fuzz(r.Conversion.Webhook)
			}
		},
	}
}

// TOODO(mjudeikis): This will go away after we rebase to 1.32 and can use new fuzzer.
func nonEmptyString(f func() string) string {
	s := f()
	switch {
	case len(s) == 0:
		return nonEmptyString(f)
	case strings.Contains(s, "."):
		return nonEmptyString(f)
	default:
		return s
	}
}
