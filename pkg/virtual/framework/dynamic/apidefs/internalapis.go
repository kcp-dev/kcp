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

package apidefs

import (
	"go.uber.org/multierr"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	endpointsopenapi "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/openapi"
	builder "k8s.io/kube-openapi/pkg/builder"
	common "k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/util"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crdpuller"
)

// InternalAPIDef provides the definition of an API to be imported from some schemes and generated OpenAPI V2 definitions
type InternalAPIDef struct {
	names     apiextensionsv1.CustomResourceDefinitionNames
	gv        schema.GroupVersion
	instance  runtime.Object
	scope     apiextensionsv1.ResourceScope
	hasStatus bool
}

// KCPInternalAPIs provides a list of InternalAPIDef for the APIs that are part of the KCP scheme and will be there in every KCP workspace
var KCPInternalAPIs = []InternalAPIDef{
	{
		names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "namespaces",
			Singular: "namespace",
			Kind:     "Namespace",
		},
		gv:        schema.GroupVersion{Group: "", Version: "v1"},
		instance:  &corev1.Namespace{},
		scope:     apiextensionsv1.ClusterScoped,
		hasStatus: true,
	},
	{
		names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "configmaps",
			Singular: "configmap",
			Kind:     "ConfigMap",
		},
		gv:       schema.GroupVersion{Group: "", Version: "v1"},
		instance: &corev1.ConfigMap{},
		scope:    apiextensionsv1.NamespaceScoped,
	},
	{
		names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "secrets",
			Singular: "secret",
			Kind:     "Secret",
		},
		gv:       schema.GroupVersion{Group: "", Version: "v1"},
		instance: &corev1.Secret{},
		scope:    apiextensionsv1.NamespaceScoped,
	},
	{
		names: apiextensionsv1.CustomResourceDefinitionNames{
			Plural:   "serviceaccounts",
			Singular: "serviceaccount",
			Kind:     "ServiceAccount",
		},
		gv:       schema.GroupVersion{Group: "", Version: "v1"},
		instance: &corev1.ServiceAccount{},
		scope:    apiextensionsv1.NamespaceScoped,
	},
}

func ImportInternalAPIs(schemes []*runtime.Scheme, openAPIDefinitionsGetters []common.GetOpenAPIDefinitions, defs ...InternalAPIDef) ([]*apiresourcev1alpha1.CommonAPIResourceSpec, error) {
	config := genericapiserver.DefaultOpenAPIConfig(func(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
		result := make(map[string]common.OpenAPIDefinition)

		for _, openAPIDefinitionsGetter := range openAPIDefinitionsGetters {
			for key, val := range openAPIDefinitionsGetter(ref) {
				result[key] = val
			}
			for key, val := range openAPIDefinitionsGetter(ref) {
				result[key] = val
			}
		}

		return result
	}, endpointsopenapi.NewDefinitionNamer(schemes...))

	var canonicalTypeNames []string
	for _, def := range defs {
		canonicalTypeNames = append(canonicalTypeNames, util.GetCanonicalTypeName(def.instance))
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

	var apis []*apiresourcev1alpha1.CommonAPIResourceSpec
	for _, def := range defs {
		gvk := def.gv.WithKind(def.names.Kind)
		var schemaProps apiextensionsv1.JSONSchemaProps
		errs := crdpuller.Convert(modelsByGKV[gvk], &schemaProps)
		if len(errs) > 0 {
			return nil, multierr.Combine(errs...)
		}
		spec := &apiresourcev1alpha1.CommonAPIResourceSpec{
			GroupVersion:                  apiresourcev1alpha1.GroupVersion(gvk.GroupVersion()),
			Scope:                         def.scope,
			CustomResourceDefinitionNames: def.names,
		}
		if def.hasStatus {
			spec.SubResources = append(spec.SubResources, apiresourcev1alpha1.SubResource{
				Name: apiresourcev1alpha1.StatusSubResourceName,
			})
		}
		if err := spec.SetSchema(&schemaProps); err != nil {
			return nil, err
		}

		apis = append(apis, spec)
	}
	return apis, nil
}
