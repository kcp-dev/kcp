/*
Copyright 2025 The kcp Authors.

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
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

type apiBindingV1alpha2 struct {
	binding *apisv1alpha2.APIBinding
}

func getAPIBindingV1alpha2(u *unstructured.Unstructured) (*apiBindingV1alpha2, error) {
	apiBinding := &apisv1alpha2.APIBinding{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiBinding); err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
	}

	return &apiBindingV1alpha2{binding: apiBinding}, nil
}

func (b apiBindingV1alpha2) BindingReference() bindingReference {
	return bindingReferenceV1alpha2{reference: &b.binding.Spec.Reference}
}

func (b apiBindingV1alpha2) Labels() map[string]string {
	return b.binding.Labels
}

func (b *apiBindingV1alpha2) SetLabels(labels map[string]string) {
	b.binding.Labels = labels
}

func (b apiBindingV1alpha2) Validate() field.ErrorList {
	return apisv1alpha2.ValidateAPIBinding(b.binding)
}

func (b apiBindingV1alpha2) ValidateUpdate(oldBinding apiBinding) field.ErrorList {
	old, ok := oldBinding.(*apiBindingV1alpha2)
	if !ok {
		panic("this should not happen")
	}

	return apisv1alpha2.ValidateAPIBindingUpdate(old.binding, b.binding)
}

func (b apiBindingV1alpha2) ToUnstructured() (map[string]interface{}, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(b.binding)
}

type bindingReferenceV1alpha2 struct {
	reference *apisv1alpha2.BindingReference
}

func (r bindingReferenceV1alpha2) HasExport() bool {
	return r.reference.Export != nil
}

func (r bindingReferenceV1alpha2) ExportName() string {
	return r.reference.Export.Name
}

func (r bindingReferenceV1alpha2) ExportPath() string {
	return r.reference.Export.Path
}

func (r bindingReferenceV1alpha2) DeepEqual(compare bindingReference) bool {
	compareV1alpha2, ok := compare.(bindingReferenceV1alpha2)
	if !ok {
		return false
	}

	return reflect.DeepEqual(r.reference, compareV1alpha2.reference)
}
