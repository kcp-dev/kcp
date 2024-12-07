/*
Copyright The KCP Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// APIServiceExportStatusApplyConfiguration represents a declarative configuration of the APIServiceExportStatus type for use
// with apply.
type APIServiceExportStatusApplyConfiguration struct {
	AcceptedNames  *v1.CustomResourceDefinitionNames `json:"acceptedNames,omitempty"`
	StoredVersions []string                          `json:"storedVersions,omitempty"`
	Conditions     *v1alpha1.Conditions              `json:"conditions,omitempty"`
}

// APIServiceExportStatusApplyConfiguration constructs a declarative configuration of the APIServiceExportStatus type for use with
// apply.
func APIServiceExportStatus() *APIServiceExportStatusApplyConfiguration {
	return &APIServiceExportStatusApplyConfiguration{}
}

// WithAcceptedNames sets the AcceptedNames field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the AcceptedNames field is set to the value of the last call.
func (b *APIServiceExportStatusApplyConfiguration) WithAcceptedNames(value v1.CustomResourceDefinitionNames) *APIServiceExportStatusApplyConfiguration {
	b.AcceptedNames = &value
	return b
}

// WithStoredVersions adds the given value to the StoredVersions field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the StoredVersions field.
func (b *APIServiceExportStatusApplyConfiguration) WithStoredVersions(values ...string) *APIServiceExportStatusApplyConfiguration {
	for i := range values {
		b.StoredVersions = append(b.StoredVersions, values[i])
	}
	return b
}

// WithConditions sets the Conditions field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Conditions field is set to the value of the last call.
func (b *APIServiceExportStatusApplyConfiguration) WithConditions(value v1alpha1.Conditions) *APIServiceExportStatusApplyConfiguration {
	b.Conditions = &value
	return b
}
