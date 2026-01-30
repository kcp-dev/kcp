/*
Copyright 2026 The KCP Authors.

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

package apiserver

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// isCRDResource checks if a CustomResourceDefinition is for the CRD type itself
// (apiextensions.k8s.io/v1.CustomResourceDefinition).
//
// This is used to trigger special handling for CRD's deeply nested, recursive schema structure.
// Without this, the OpenAPI builder would generate an incomplete schema where nested fields like
// spec.versions[].schema.openAPIV3Schema.properties are truncated to <Object> instead of showing
// their actual types.
//
// When this returns true, ensureCompleteSchemaGeneration() is called to set
// XPreserveUnknownFields=false on the CRD's schema, forcing the OpenAPI builder to generate
// the complete schema tree
//
// See: https://github.com/kcp-dev/kcp/issues/3389
func isCRDResource(crd *apiextensionsv1.CustomResourceDefinition) bool {
	return crd.Spec.Group == "apiextensions.k8s.io" && crd.Spec.Names.Kind == "CustomResourceDefinition"
}

func isCRDAPIResourceSchema(apiResourceSchema *apisv1alpha1.APIResourceSchema) bool {
	return apiResourceSchema.Spec.Group == "apiextensions.k8s.io" && apiResourceSchema.Spec.Names.Kind == "CustomResourceDefinition"
}
