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

package apiexport

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func toAPIResourceSchema(r *apiresourcev1alpha1.NegotiatedAPIResource, name string) *apisv1alpha1.APIResourceSchema {
	group := r.Spec.CommonAPIResourceSpec.GroupVersion.Group
	if group == "core" {
		group = ""
	}
	schema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: r.Spec.CommonAPIResourceSpec.CustomResourceDefinitionNames,
			Scope: r.Spec.CommonAPIResourceSpec.Scope,
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    r.Spec.CommonAPIResourceSpec.GroupVersion.Version,
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: r.Spec.CommonAPIResourceSpec.OpenAPIV3Schema.Raw,
					},
				},
			},
		},
	}
	for _, sr := range r.Spec.CommonAPIResourceSpec.SubResources {
		switch sr.Name {
		case apiresourcev1alpha1.ScaleSubResourceName:
			schema.Spec.Versions[0].Subresources.Scale = &apiextensionsv1.CustomResourceSubresourceScale{
				// TODO(sttts): change NegotiatedAPIResource and APIResourceImport to preserve the paths from the CRDs in the pcluster, or have custom logic for native resources. Here, we can only guess.
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
		case apiresourcev1alpha1.StatusSubResourceName:
			schema.Spec.Versions[0].Subresources.Status = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}
	}
	schema.Spec.Versions[0].AdditionalPrinterColumns = r.Spec.CommonAPIResourceSpec.ColumnDefinitions.ToCustomResourceColumnDefinitions()

	if value, found := r.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; found {
		schema.Annotations = map[string]string{
			apiextensionsv1.KubeAPIApprovedAnnotation: value,
		}
	}

	return schema
}
