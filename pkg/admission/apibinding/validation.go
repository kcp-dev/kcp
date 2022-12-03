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

package apibinding

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// ValidateAPIBinding validates an APIBinding.
func ValidateAPIBinding(apiBinding *apisv1alpha1.APIBinding) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, ValidateAPIBindingReference(apiBinding.Spec.Reference, field.NewPath("spec", "reference"))...)

	return allErrs
}

// ValidateAPIBindingUpdate validates an updated APIBinding.
func ValidateAPIBindingUpdate(oldBinding, newBinding *apisv1alpha1.APIBinding) field.ErrorList {
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
func ValidateAPIBindingReference(reference apisv1alpha1.BindingReference, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// For now, field "export" is required via OpenAPI. But just in case...
	if reference.Export != nil {
		// These are required by OpenAPI, but just in case...
		if reference.Export.Path == "" {
			allErrs = append(allErrs, field.Required(path.Child("export").Child("path"), ""))
		}

		if reference.Export.Name == "" {
			allErrs = append(allErrs, field.Required(path.Child("export").Child("name"), ""))
		}
	}

	return allErrs
}
