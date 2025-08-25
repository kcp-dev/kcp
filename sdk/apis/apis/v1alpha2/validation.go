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
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidateAPIBinding validates an APIBinding.
func ValidateAPIBinding(apiBinding *APIBinding) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, ValidateAPIBindingReference(apiBinding.Spec.Reference, field.NewPath("spec", "reference"))...)
	allErrs = append(allErrs, ValidateAPIBindingPermissionClaims(apiBinding.Spec.PermissionClaims, field.NewPath("spec", "permissionClaims"))...)

	return allErrs
}

// ValidateAPIBindingUpdate validates an updated APIBinding.
func ValidateAPIBindingUpdate(oldBinding, newBinding *APIBinding) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, ValidateAPIBinding(newBinding)...)

	if oldBinding.Status.Phase != "" && newBinding.Status.Phase == "" {
		allErrs = append(allErrs,
			field.Forbidden(
				field.NewPath("status", "phase"),
				fmt.Sprintf("cannot transition from %q to %q", oldBinding.Status.Phase, newBinding.Status.Phase),
			),
		)
	}

	return allErrs
}

// ValidateAPIBindingReference validates an APIBinding's BindingReference.
func ValidateAPIBindingReference(reference BindingReference, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if reference.Export == nil {
		allErrs = append(allErrs, field.Required(path.Child("export"), ""))
	} else if reference.Export.Name == "" {
		allErrs = append(allErrs, field.Required(path.Child("export").Child("name"), ""))
	}

	return allErrs
}

// ValidateAPIBindingPermissionClaims validates an APIBinding's PermissionClaims.
func ValidateAPIBindingPermissionClaims(permissionClaims []AcceptablePermissionClaim, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for i := range permissionClaims {
		claimPath := path.Index(i)

		if permissionClaims[i].Selector.MatchAll {
			if len(permissionClaims[i].Selector.MatchLabels) > 0 {
				allErrs = append(allErrs, field.Invalid(claimPath.Child("selector").Child("matchLabels"), permissionClaims[i].Selector, "matchLabels cannot be used with matchAll"))
			}
			if len(permissionClaims[i].Selector.MatchExpressions) > 0 {
				allErrs = append(allErrs, field.Invalid(claimPath.Child("selector").Child("matchExpressions"), permissionClaims[i].Selector, "matchExpressions cannot be used with matchAll"))
			}
		} else if len(permissionClaims[i].Selector.MatchLabels) == 0 && len(permissionClaims[i].Selector.MatchExpressions) == 0 {
			allErrs = append(allErrs, field.Required(claimPath.Child("selector"), "either one of matchAll, matchLabels, or matchExpressions must be set"))
		}
	}

	return allErrs
}
