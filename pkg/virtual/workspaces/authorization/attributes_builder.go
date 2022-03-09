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

package authorization

import (
	"path"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

// AttributesBuilder is a helper for creating an authorizer.AttributesRecord.
type AttributesBuilder struct {
	*authorizer.AttributesRecord
}

// NewAttributesBuilder creates a new AttributesBuilder with a zero-value authorizer.AttributesRecord.
func NewAttributesBuilder() *AttributesBuilder {
	return &AttributesBuilder{
		AttributesRecord: &authorizer.AttributesRecord{
			ResourceRequest: true,
		},
	}
}

// Verb sets the verb on the builder's AttributesRecord.
func (b *AttributesBuilder) Verb(verb string) *AttributesBuilder {
	b.AttributesRecord.Verb = verb
	return b
}

// Resource sets APIVersion, APIGroup, Resource, and Subresource on the builder's AttributesRecord.
func (b *AttributesBuilder) Resource(gvr schema.GroupVersionResource, subresources ...string) *AttributesBuilder {
	b.APIVersion = gvr.Version
	b.APIGroup = gvr.Group
	b.AttributesRecord.Resource = gvr.Resource
	b.Subresource = path.Join(subresources...)
	return b
}

// Name sets the name on the builder's AttributesRecord.
func (b *AttributesBuilder) Name(name string) *AttributesBuilder {
	b.AttributesRecord.Name = name
	return b
}
