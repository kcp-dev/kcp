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

package v1alpha1

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// CRDToAPIResourceSchema converts a CustomResourceDefinition to an APIResourceSchema. The name of the returned
// APIResourceSchema is in the form of <prefix>.<crd.Name>.
func CRDToAPIResourceSchema(crd *apiextensionsv1.CustomResourceDefinition, prefix string) (*APIResourceSchema, error) {
	name := prefix + "." + crd.Name

	if msgs := validation.IsDNS1123Subdomain(name); len(msgs) > 0 {
		var errs []error

		for _, msg := range msgs {
			errs = append(errs, field.Invalid(field.NewPath("metadata", "name"), name, msg))
		}

		return nil, errors.NewAggregate(errs)
	}

	apiResourceSchema := &APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: APIResourceSchemaSpec{
			Group: crd.Spec.Group,
			Names: crd.Spec.Names,
			Scope: crd.Spec.Scope,
		},
	}

	for i := range crd.Spec.Versions {
		crdVersion := crd.Spec.Versions[i]

		apiResourceVersion := APIResourceVersion{
			Name:                     crdVersion.Name,
			Served:                   crdVersion.Served,
			Storage:                  crdVersion.Storage,
			Deprecated:               crdVersion.Deprecated,
			DeprecationWarning:       crdVersion.DeprecationWarning,
			AdditionalPrinterColumns: crdVersion.AdditionalPrinterColumns,
		}

		if crdVersion.Schema != nil && crdVersion.Schema.OpenAPIV3Schema != nil {
			if err := apiResourceVersion.SetSchema(crdVersion.Schema.OpenAPIV3Schema); err != nil {
				return nil, fmt.Errorf("error converting schema for version %q: %w", crdVersion.Name, err)
			}
		}

		if crdVersion.Subresources != nil {
			apiResourceVersion.Subresources = *crdVersion.Subresources
		}

		apiResourceSchema.Spec.Versions = append(apiResourceSchema.Spec.Versions, apiResourceVersion)
	}

	return apiResourceSchema, nil
}
