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

package initialization

import (
	"embed"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpscheme "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
)

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

func CustomResourceDefinition(gr metav1.GroupResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := rawCustomResourceDefinitions.ReadFile(fmt.Sprintf("%s_%s.yaml", gr.Group, gr.Resource))
	if err != nil {
		return nil, fmt.Errorf("could not read CRD %s: %w", gr.String(), err)
	}

	expectedGvk := &schema.GroupVersionKind{Group: apiextensionsv1.GroupName, Version: "v1", Kind: "CustomResourceDefinition"}

	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw CRD %s: %w", gr.String(), err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded CRD %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}

	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil, fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gr.String(), obj, &apiextensionsv1.CustomResourceDefinition{})
	}

	return crd, nil
}

func APIResourceSchema(gr metav1.GroupResource) (*apisv1alpha1.APIResourceSchema, error) {
	raw, err := rawCustomResourceDefinitions.ReadFile(fmt.Sprintf("apiresourceschema-%s.yaml", gr.String()))
	if err != nil {
		return nil, fmt.Errorf("could not read APIResourceSchema %s: %w", gr.String(), err)
	}

	expectedGvk := &schema.GroupVersionKind{Group: apis.GroupName, Version: "v1alpha1", Kind: "APIResourceSchema"}

	obj, gvk, err := kcpscheme.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apisv1alpha1.APIResourceSchema{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw APIResourceSchema %s: %w", gr.String(), err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded APIResourceSchema %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}

	apiResourceSchema, ok := obj.(*apisv1alpha1.APIResourceSchema)
	if !ok {
		return nil, fmt.Errorf("decoded APIResourceSchema %s into incorrect type, got %T, wanted %T", gr.String(), obj, &apisv1alpha1.APIResourceSchema{})
	}

	return apiResourceSchema, nil
}
