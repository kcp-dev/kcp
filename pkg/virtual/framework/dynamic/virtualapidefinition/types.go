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
	"net/http"

	// "k8s.io/apiserver/pkg/endpoints/handlers"
	// "k8s.io/apiserver/pkg/registry/rest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

// VirtualAPIDefinition provides access to all the information needed to serve a given API resource.
type VirtualAPIDefinition interface {
	// GetOpenAPISpec
	GetAPIGroups(ctx context.Context) ([]metav1.APIGroup, error)
	GetAPIResources(ctx context.Context) ([]metav1.APIResource, error)
	GetProxy(ctx context.Context) (http.Handler, error)

	// TearDown shuts down long-running connections.
	TearDown()
}

// APIDefinitionSet contains the APIDefinition objects for the APIs of an API domain.
type VirtualAPIDefinitionSet map[schema.GroupResource]VirtualAPIDefinition

// VirtualAPIDefinitionSetGetter provides access to the API definitions of a API domain, based on the API domain key.
type VirtualAPIDefinitionSetGetter interface {
	GetVirtualAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (virtualApis VirtualAPIDefinitionSet, apisExist bool, err error)
}
