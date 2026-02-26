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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

type apiBindingV1alpha1 struct {
	binding *apisv1alpha1.APIBinding
}

func getAPIBindingV1alpha1(u *unstructured.Unstructured) (*apiBindingV1alpha1, error) {
	apiBinding := &apisv1alpha1.APIBinding{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiBinding); err != nil {
		return nil, fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
	}

	return &apiBindingV1alpha1{binding: apiBinding}, nil
}

func (b apiBindingV1alpha1) BindingReference() bindingReference {
	return bindingReferenceV1alpha1{reference: &b.binding.Spec.Reference}
}

func (b apiBindingV1alpha1) Labels() map[string]string {
	return b.binding.Labels
}

func (b *apiBindingV1alpha1) SetLabels(labels map[string]string) {
	b.binding.Labels = labels
}

func (b *apiBindingV1alpha1) Validate() field.ErrorList {
	return apisv1alpha1.ValidateAPIBinding(b.binding)
}

func (b apiBindingV1alpha1) ValidateUpdate(oldBinding apiBinding) field.ErrorList {
	old, ok := oldBinding.(*apiBindingV1alpha1)
	if !ok {
		panic("this should not happen")
	}

	return apisv1alpha1.ValidateAPIBindingUpdate(old.binding, b.binding)
}

func (b apiBindingV1alpha1) ToUnstructured() (map[string]interface{}, error) {
	return runtime.DefaultUnstructuredConverter.ToUnstructured(b.binding)
}

type bindingReferenceV1alpha1 struct {
	reference *apisv1alpha1.BindingReference
}

func (r bindingReferenceV1alpha1) HasExport() bool {
	return r.reference.Export != nil
}

func (r bindingReferenceV1alpha1) ExportName() string {
	if r.reference.Export == nil {
		return ""
	}

	return r.reference.Export.Name
}

func (r bindingReferenceV1alpha1) ExportPath() string {
	if r.reference.Export == nil {
		return ""
	}

	return r.reference.Export.Path
}

func (r bindingReferenceV1alpha1) DeepEqual(compare bindingReference) bool {
	compareV1alpha1, ok := compare.(bindingReferenceV1alpha1)
	if !ok {
		return false
	}

	return reflect.DeepEqual(r.reference, compareV1alpha1.reference)
}
