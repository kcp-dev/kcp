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
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// APIServiceExportVersionApplyConfiguration represents a declarative configuration of the APIServiceExportVersion type for use
// with apply.
type APIServiceExportVersionApplyConfiguration struct {
	Name                     *string                                   `json:"name,omitempty"`
	Served                   *bool                                     `json:"served,omitempty"`
	Storage                  *bool                                     `json:"storage,omitempty"`
	Deprecated               *bool                                     `json:"deprecated,omitempty"`
	DeprecationWarning       *string                                   `json:"deprecationWarning,omitempty"`
	Schema                   *APIServiceExportSchemaApplyConfiguration `json:"schema,omitempty"`
	Subresources             *v1.CustomResourceSubresources            `json:"subresources,omitempty"`
	AdditionalPrinterColumns []v1.CustomResourceColumnDefinition       `json:"additionalPrinterColumns,omitempty"`
}

// APIServiceExportVersionApplyConfiguration constructs a declarative configuration of the APIServiceExportVersion type for use with
// apply.
func APIServiceExportVersion() *APIServiceExportVersionApplyConfiguration {
	return &APIServiceExportVersionApplyConfiguration{}
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithName(value string) *APIServiceExportVersionApplyConfiguration {
	b.Name = &value
	return b
}

// WithServed sets the Served field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Served field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithServed(value bool) *APIServiceExportVersionApplyConfiguration {
	b.Served = &value
	return b
}

// WithStorage sets the Storage field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Storage field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithStorage(value bool) *APIServiceExportVersionApplyConfiguration {
	b.Storage = &value
	return b
}

// WithDeprecated sets the Deprecated field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Deprecated field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithDeprecated(value bool) *APIServiceExportVersionApplyConfiguration {
	b.Deprecated = &value
	return b
}

// WithDeprecationWarning sets the DeprecationWarning field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the DeprecationWarning field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithDeprecationWarning(value string) *APIServiceExportVersionApplyConfiguration {
	b.DeprecationWarning = &value
	return b
}

// WithSchema sets the Schema field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Schema field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithSchema(value *APIServiceExportSchemaApplyConfiguration) *APIServiceExportVersionApplyConfiguration {
	b.Schema = value
	return b
}

// WithSubresources sets the Subresources field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Subresources field is set to the value of the last call.
func (b *APIServiceExportVersionApplyConfiguration) WithSubresources(value v1.CustomResourceSubresources) *APIServiceExportVersionApplyConfiguration {
	b.Subresources = &value
	return b
}

// WithAdditionalPrinterColumns adds the given value to the AdditionalPrinterColumns field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the AdditionalPrinterColumns field.
func (b *APIServiceExportVersionApplyConfiguration) WithAdditionalPrinterColumns(values ...v1.CustomResourceColumnDefinition) *APIServiceExportVersionApplyConfiguration {
	for i := range values {
		b.AdditionalPrinterColumns = append(b.AdditionalPrinterColumns, values[i])
	}
	return b
}
