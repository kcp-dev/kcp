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

package v1alpha2

// APIExportSpecApplyConfiguration represents a declarative configuration of the APIExportSpec type for use
// with apply.
type APIExportSpecApplyConfiguration struct {
	ResourceSchemas         []ResourceSchemaApplyConfiguration         `json:"resourceSchemas,omitempty"`
	Identity                *IdentityApplyConfiguration                `json:"identity,omitempty"`
	MaximalPermissionPolicy *MaximalPermissionPolicyApplyConfiguration `json:"maximalPermissionPolicy,omitempty"`
	PermissionClaims        []PermissionClaimApplyConfiguration        `json:"permissionClaims,omitempty"`
}

// APIExportSpecApplyConfiguration constructs a declarative configuration of the APIExportSpec type for use with
// apply.
func APIExportSpec() *APIExportSpecApplyConfiguration {
	return &APIExportSpecApplyConfiguration{}
}

// WithResourceSchemas adds the given value to the ResourceSchemas field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the ResourceSchemas field.
func (b *APIExportSpecApplyConfiguration) WithResourceSchemas(values ...*ResourceSchemaApplyConfiguration) *APIExportSpecApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithResourceSchemas")
		}
		b.ResourceSchemas = append(b.ResourceSchemas, *values[i])
	}
	return b
}

// WithIdentity sets the Identity field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Identity field is set to the value of the last call.
func (b *APIExportSpecApplyConfiguration) WithIdentity(value *IdentityApplyConfiguration) *APIExportSpecApplyConfiguration {
	b.Identity = value
	return b
}

// WithMaximalPermissionPolicy sets the MaximalPermissionPolicy field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the MaximalPermissionPolicy field is set to the value of the last call.
func (b *APIExportSpecApplyConfiguration) WithMaximalPermissionPolicy(value *MaximalPermissionPolicyApplyConfiguration) *APIExportSpecApplyConfiguration {
	b.MaximalPermissionPolicy = value
	return b
}

// WithPermissionClaims adds the given value to the PermissionClaims field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, values provided by each call will be appended to the PermissionClaims field.
func (b *APIExportSpecApplyConfiguration) WithPermissionClaims(values ...*PermissionClaimApplyConfiguration) *APIExportSpecApplyConfiguration {
	for i := range values {
		if values[i] == nil {
			panic("nil value passed to WithPermissionClaims")
		}
		b.PermissionClaims = append(b.PermissionClaims, *values[i])
	}
	return b
}
