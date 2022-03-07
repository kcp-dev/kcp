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

	if phaseOrdinal[oldBinding.Status.Phase] > phaseOrdinal[newBinding.Status.Phase] {
		allErrs = append(allErrs,
			field.Forbidden(
				field.NewPath("status", "phase"),
				fmt.Sprintf("cannot transition from %q to %q", oldBinding.Status.Phase, newBinding.Status.Phase),
			),
		)
	}

	return allErrs
}

// ValidateAPIBindingReference validates an APIBinding's ExportReference.
func ValidateAPIBindingReference(reference apisv1alpha1.ExportReference, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if workspace := reference.Workspace; workspace != nil {
		if workspace.WorkspaceName == "" {
			allErrs = append(allErrs, field.Required(path.Child("name"), ""))
		}

		if workspace.ExportName == "" {
			allErrs = append(allErrs, field.Required(path.Child("exportName"), ""))
		}
	}

	return allErrs
}
