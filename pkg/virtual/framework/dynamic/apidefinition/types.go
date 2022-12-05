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

package apidefinition

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/registry/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

// APIDefinition provides access to all the information needed to serve a given API resource
type APIDefinition interface {
	// GetAPIResourceSchema returns the API schema this definition serves.
	GetAPIResourceSchema() *apisv1alpha1.APIResourceSchema

	// GetClusterName provides the name of the logical cluster where the resource specification comes from.
	GetClusterName() logicalcluster.Name

	// GetStorage provides the REST storage used to serve the resource.
	GetStorage() rest.Storage

	// GetSubResourceStorage provides the REST storage required to serve the given sub-resource.
	GetSubResourceStorage(subresource string) rest.Storage

	// GetRequestScope provides the handlers.RequestScope required to serve the resource.
	GetRequestScope() *handlers.RequestScope

	// GetSubResourceRequestScope provides the handlers.RequestScope required to serve the given sub-resource.
	GetSubResourceRequestScope(subresource string) *handlers.RequestScope

	// TearDown shuts down long-running connections.
	TearDown()
}

// APIDefinitionSet contains the APIDefinition objects for the APIs of an API domain.
type APIDefinitionSet map[schema.GroupVersionResource]APIDefinition

// APIDefinitionSetGetter provides access to the API definitions of a API domain, based on the API domain key.
type APIDefinitionSetGetter interface {
	GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis APIDefinitionSet, apisExist bool, err error)
}
