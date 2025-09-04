/*
Copyright 2025 The KCP Authors.

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

package virtualapidefinition

import (
	"context"

	// "k8s.io/apiserver/pkg/endpoints/handlers"
	// "k8s.io/apiserver/pkg/registry/rest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	// "github.com/kcp-dev/logicalcluster/v3"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	// apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	// discoveryapi "k8s.io/apiserver/pkg/endpoints/discovery"
)

// VirtualAPIDefinition provides access to all the information needed to serve a given API resource.
type VirtualAPIDefinition interface {
	// GetOpenAPISpec
	GetAPIGroups() ([]metav1.APIGroup, error)
	GetAPIResources() ([]metav1.APIResource, error)

	// TearDown shuts down long-running connections.
	TearDown()
}

// APIDefinitionSet contains the APIDefinition objects for the APIs of an API domain.
type VirtualAPIDefinitionSet []VirtualAPIDefinition

// VirtualAPIDefinitionSetGetter provides access to the API definitions of a API domain, based on the API domain key.
type VirtualAPIDefinitionSetGetter interface {
	GetVirtualAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (virtualApis VirtualAPIDefinitionSet, apisExist bool, err error)
}
