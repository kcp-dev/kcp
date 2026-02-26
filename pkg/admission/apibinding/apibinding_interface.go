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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

type apiBinding interface {
	BindingReference() bindingReference
	Labels() map[string]string
	SetLabels(labels map[string]string)
	Validate() field.ErrorList
	ValidateUpdate(oldBinding apiBinding) field.ErrorList
	ToUnstructured() (map[string]interface{}, error)
}

type bindingReference interface {
	HasExport() bool
	ExportName() string
	ExportPath() string
	DeepEqual(compare bindingReference) bool
}

func getAPIBinding(u *unstructured.Unstructured, preferredVersion string) (apiBinding, error) {
	switch preferredVersion {
	case "v1alpha1":
		return getAPIBindingV1alpha1(u)

	case "v1alpha2":
		return getAPIBindingV1alpha2(u)

	default:
		return nil, fmt.Errorf("version %q is not supported by this admission plugin", preferredVersion)
	}
}
