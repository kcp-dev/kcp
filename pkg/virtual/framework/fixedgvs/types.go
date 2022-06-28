/*
Copyright 2021 The KCP Authors.

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

package fixedgvs

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	reststorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	openapicommon "k8s.io/kube-openapi/pkg/common"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
)

// RestStorageBuilder is a function that builds a REST Storage based on the config of the
// dedicated delegated APIServer is will be created on.
type RestStorageBuilder func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (reststorage.Storage, error)

// GroupVersionAPISet describes the set of APIs that should be added in a given group/version.
// This allows specifying the logic that should build related REST storages,
// as well as the additional schemes required to register the REST storages.
type GroupVersionAPISet struct {
	GroupVersion schema.GroupVersion

	// AddToScheme adds the additional schemes required to register the REST storages
	AddToScheme func(*runtime.Scheme) error

	// OpenAPIDefinitions contains the OpenAPI v2 definitions of resources provided by the REST storages
	OpenAPIDefinitions openapicommon.GetOpenAPIDefinitions

	// BootstrapRestResources bootstraps the various Rest storage builders (one for each REST resource name),
	// that will be later registered in dedicated delegated APIServer by the framework.
	// This bootstrapping may include creating active objects like controllers,
	// adding poststart hooks into the rootAPIServerConfig, etc ...
	BootstrapRestResources func(rootAPIServerConfig genericapiserver.CompletedConfig) (map[string]RestStorageBuilder, error)
}

// FixedGroupVersionsVirtualWorkspace is an implementation of
// the VirtualWorkspace interface, which allows adding well-defined APIs
// in a limited number of group/versions, implemented as Rest storages.
type FixedGroupVersionsVirtualWorkspace struct {
	framework.RootPathResolver
	authorizer.Authorizer
	framework.ReadyChecker
	GroupVersionAPISets []GroupVersionAPISet
}
