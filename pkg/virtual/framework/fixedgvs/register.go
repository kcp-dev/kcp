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
	openapibuilder "k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/kube-openapi/pkg/builder"
	"k8s.io/kube-openapi/pkg/common/restfuladapter"
	"k8s.io/kube-openapi/pkg/handler"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs/apiserver"
)

func (vw *FixedGroupVersionsVirtualWorkspace) Register(vwName string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	var vwGroupManager discovery.GroupManager
	var firstAPIServer *genericapiserver.GenericAPIServer
	var openAPISpecs []*spec.Swagger

	for _, groupVersionAPISet := range vw.GroupVersionAPISets {
		restStorageBuilders, err := groupVersionAPISet.BootstrapRestResources(rootAPIServerConfig)
		if err != nil {
			return nil, err
		}

		cfg := &apiserver.GroupVersionAPIServerConfig{
			GenericConfig: &genericapiserver.RecommendedConfig{Config: *rootAPIServerConfig.Config, SharedInformerFactory: rootAPIServerConfig.SharedInformerFactory},
			ExtraConfig: apiserver.ExtraConfig{
				GroupVersion:    groupVersionAPISet.GroupVersion,
				StorageBuilders: make(map[string]func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (restStorage.Storage, error)),
			},
		}
		for resourceName, builder := range restStorageBuilders {
			cfg.ExtraConfig.StorageBuilders[resourceName] = builder
		}

		scheme := runtime.NewScheme()
		metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})
		if err := groupVersionAPISet.AddToScheme(scheme); err != nil {
			return nil, err
		}

		if groupVersionAPISet.OpenAPIDefinitions != nil {
			cfg.GenericConfig.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(groupVersionAPISet.OpenAPIDefinitions, openapi.NewDefinitionNamer(scheme))
			cfg.GenericConfig.OpenAPIConfig.Info.Title = "KCP Virtual Workspace for " + vwName
			cfg.GenericConfig.SkipOpenAPIInstallation = true
		}

		// We don't want any poststart hooks at the level of a GroupVersionAPIServer.
		// In the current design, PostStartHooks are only added at the top level RootAPIServer.
		// So let's drop the PostStartHooks from the GroupVersionAPIServerConfig since they are simply copied
		// from the RootAPIServerConfig
		cfg.GenericConfig.PostStartHooks = map[string]genericapiserver.PostStartHookConfigEntry{}
		config := cfg.Complete()

		if vwGroupManager != nil {
			// If this GroupVersionAPIServer is not the first one for
			// a given virtual workspace, then disable discover and reuse
			// the GroupManager of the first one.
			config.GenericConfig.EnableDiscovery = false
		}

		server, err := config.New(vwName, vwGroupManager, scheme, delegateAPIServer)
		if err != nil {
			return nil, err
		}

		if groupVersionAPISet.OpenAPIDefinitions != nil {
			spec, err := builder.BuildOpenAPISpecFromRoutes(restfuladapter.AdaptWebServices(server.GenericAPIServer.Handler.GoRestfulContainer.RegisteredWebServices()), config.GenericConfig.OpenAPIConfig)
			if err != nil {
				return nil, err
			}
			spec.Definitions = handler.PruneDefaults(spec.Definitions)
			openAPISpecs = append(openAPISpecs, spec)
		}

		if vwGroupManager == nil && server.GenericAPIServer.DiscoveryGroupManager != nil {
			// If this GroupVersionAPIServer is the first one for
			// a given virtual workspace, then grab its DiscoveryGroupManager
			// to reuse it in the next GroupVersionAPIServers for the virtual workspace.
			vwGroupManager = server.GenericAPIServer.DiscoveryGroupManager
		}
		if firstAPIServer == nil {
			// If this GroupVersionAPIServer is the first one for
			// a given virtual workspace, then store it in order to later on
			// install on it the OpenAPIServiceProvider with the merge OpenAPI Specs
			// of all the GV API Sets.
			firstAPIServer = server.GenericAPIServer
		}
		delegateAPIServer = server.GenericAPIServer
	}

	if len(openAPISpecs) > 0 && firstAPIServer != nil {
		mergedSpec := openAPISpecs[0]
		if len(openAPISpecs) > 1 {
			var err error
			mergedSpec, err = openapibuilder.MergeSpecs(mergedSpec, openAPISpecs[1:]...)
			if err != nil {
				return nil, err
			}
		}
		openAPIService := handler.NewOpenAPIService(mergedSpec)
		openAPIService.RegisterOpenAPIVersionedService("/openapi/v2", firstAPIServer.Handler.NonGoRestfulMux)
	}

	return delegateAPIServer, nil
}
