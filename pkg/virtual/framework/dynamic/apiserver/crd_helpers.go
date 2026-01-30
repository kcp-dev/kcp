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
// This is used to trigger special handling for the deeply nested, recursive schema structure
// of a CRD. When generating the OpenAPI spec, the builder would otherwise truncate
// spec.versions[].schema.openAPIV3Schema to <Object>.
//
// When this returns true, we explicitly patch the OpenAPI output to include
// x-kubernetes-preserve-unknown-fields: true on openAPIV3Schema. This tells clients
// (like kubectl) that the field accepts arbitrary objects without forcing the OpenAPI
// builder to recursively unroll the massive JSONSchemaProps tree which cpuld crush performance.
//
// See: https://github.com/kcp-dev/kcp/issues/3389
func isCRDResource(crd *apiextensionsv1.CustomResourceDefinition) bool {
	return crd.Spec.Group == "apiextensions.k8s.io" && crd.Spec.Names.Kind == "CustomResourceDefinition"
}

func isCRDAPIResourceSchema(apiResourceSchema *apisv1alpha1.APIResourceSchema) bool {
	return apiResourceSchema.Spec.Group == "apiextensions.k8s.io" && apiResourceSchema.Spec.Names.Kind == "CustomResourceDefinition"
}
