/*
Copyright 2023 The KCP Authors.

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

package apiexportendpointslice

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// ValidateAPIExportEndpointSlice validates an APIExport.
func ValidateAPIExportEndpointSlice(slice *apisv1alpha1.APIExportEndpointSlice) field.ErrorList {
	allErrs := field.ErrorList{}

	allErrs = append(allErrs, ValidateAPIExportEndpointSliceReference(slice.Spec.APIExport, field.NewPath("spec", "export"))...)

	return allErrs
}

// ValidateAPIExportEndpointSliceReference validates an APIExportEndpointSlice's APIExport reference.
func ValidateAPIExportEndpointSliceReference(reference apisv1alpha1.ExportBindingReference, path *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	if reference.Name == "" {
		allErrs = append(allErrs, field.Required(path.Child("name"), ""))
	}

	return allErrs
}
