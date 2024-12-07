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
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// APIServiceExportRequestSpecApplyConfiguration represents a declarative configuration of the APIServiceExportRequestSpec type for use
// with apply.
type APIServiceExportRequestSpecApplyConfiguration struct {
	Parameters *runtime.RawExtension                               `json:"parameters,omitempty"`
	Resources  []APIServiceExportRequestResourceApplyConfiguration `json:"resources,omitempty"`
}

// APIServiceExportRequestSpecApplyConfiguration constructs a declarative configuration of the APIServiceExportRequestSpec type for use with
// apply.
func APIServiceExportRequestSpec() *APIServiceExportRequestSpecApplyConfiguration {
	return &APIServiceExportRequestSpecApplyConfiguration{}
}

// WithParameters sets the Parameters field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Parameters field is set to the value of the last call.
func (b *APIServiceExportRequestSpecApplyConfiguration) WithParameters(value runtime.RawExtension) *APIServiceExportRequestSpecApplyConfiguration {
	b.Parameters = &value
	return b
}

// WithResources adds the given value to the Resources field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the Resources field.
func (b *APIServiceExportRequestSpecApplyConfiguration) WithResources(values ...*APIServiceExportRequestResourceApplyConfiguration) *APIServiceExportRequestSpecApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithResources")
		}
		b.Resources = append(b.Resources, *values[i])
	}
	return b
}
