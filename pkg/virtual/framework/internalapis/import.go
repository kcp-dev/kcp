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

package internalapis

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	endpointsopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/openapi"
	"k8s.io/kube-openapi/pkg/builder"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/util"

	"github.com/kcp-dev/kcp/pkg/crdpuller"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// InternalAPI describes an API to be imported from some schemes and generated OpenAPI V2 definitions.
type InternalAPI struct {
	Names         apiextensionsv1.CustomResourceDefinitionNames
	GroupVersion  schema.GroupVersion
	Instance      runtime.Object
	ResourceScope apiextensionsv1.ResourceScope
	HasStatus     bool
}

func CreateAPIResourceSchemas(schemes []*runtime.Scheme, openAPIDefinitionsGetters []common.GetOpenAPIDefinitions, defs ...InternalAPI) (map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	config := genericapiserver.DefaultOpenAPIConfig(func(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
		result := make(map[string]common.OpenAPIDefinition)

		for _, openAPIDefinitionsGetter := range openAPIDefinitionsGetters {
			for key, val := range openAPIDefinitionsGetter(ref) {
				result[key] = val
			}
		}

		return result
	}, endpointsopenapi.NewDefinitionNamer(schemes...))

	canonicalTypeNames := make([]string, 0, len(defs))
	for _, def := range defs {
		canonicalTypeNames = append(canonicalTypeNames, util.GetCanonicalTypeName(def.Instance))
	}
	swagger, err := builder.BuildOpenAPIDefinitionsForResources(config, canonicalTypeNames...)
	if err != nil {
		return nil, err
	}
	swagger.Info.Version = "1.0"
	models, err := openapi.ToProtoModels(swagger)
	if err != nil {
		return nil, err
	}

	modelsByGKV, err := endpointsopenapi.GetModelsByGKV(models)
	if err != nil {
		return nil, err
	}

	apis := map[apisv1alpha1.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for _, def := range defs {
		gvk := def.GroupVersion.WithKind(def.Names.Kind)
		var schemaProps apiextensionsv1.JSONSchemaProps
		errs := crdpuller.Convert(modelsByGKV[gvk], &schemaProps)
		if len(errs) > 0 {
			return nil, errors.NewAggregate(errs)
		}
		group := def.GroupVersion.Group
		if group == "" {
			group = "core"
		}

		version := apisv1alpha1.APIResourceVersion{
			Name:    def.GroupVersion.Version,
			Served:  true,
			Storage: true,
			Schema:  runtime.RawExtension{},
		}

		if def.HasStatus {
			version.Subresources.Status = &apiextensionsv1.CustomResourceSubresourceStatus{}
		}

		if err := version.SetSchema(&schemaProps); err != nil {
			return nil, err
		}

		if ars, exists := apis[apisv1alpha1.GroupResource{
			Group:    group,
			Resource: def.Names.Plural,
		}]; exists {
			ars.Spec.Versions = append(ars.Spec.Versions, version)
			continue
		}

		spec := &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("internal.%s.%s", def.Names.Plural, group),
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group:    def.GroupVersion.Group,
				Names:    def.Names,
				Scope:    def.ResourceScope,
				Versions: []apisv1alpha1.APIResourceVersion{version},
			},
		}

		apis[apisv1alpha1.GroupResource{
			Group:    gvk.Group,
			Resource: def.Names.Plural,
		}] = spec
	}

	return apis, nil
}
