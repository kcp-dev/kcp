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
	"k8s.io/apiserver/pkg/endpoints/discovery"
	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/fixedgvs/apiserver"
)

func (vw *FixedGroupVersionsVirtualWorkspace) Register(rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	var vwGroupManager discovery.GroupManager
	for _, groupVersionAPISet := range vw.GroupVersionAPISets {
		restStorageBuilders, err := groupVersionAPISet.BootstrapRestResources(rootAPIServerConfig)
		if err != nil {
			return nil, err
		}

		cfg := &apiserver.GroupVersionAPIServerConfig{
			GenericConfig: &genericapiserver.RecommendedConfig{Config: *rootAPIServerConfig.Config, SharedInformerFactory: rootAPIServerConfig.SharedInformerFactory},
			ExtraConfig: apiserver.ExtraConfig{
				GroupVersion:    groupVersionAPISet.GroupVersion,
				AddToScheme:     groupVersionAPISet.AddToScheme,
				StorageBuilders: make(map[string]func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (restStorage.Storage, error)),
			},
		}
		for resourceName, builder := range restStorageBuilders {
			cfg.ExtraConfig.StorageBuilders[resourceName] = builder
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
		server, err := config.New(vw.Name, vwGroupManager, delegateAPIServer)
		if err != nil {
			return nil, err
		}
		if vwGroupManager == nil {
			// If this GroupVersionAPIServer is the first one for
			// a given virtual workspace, then grab its DiscoveryGroupManager
			// to reuse it in the next GroupVersionAPIServers for the virtual workspace.
			vwGroupManager = server.GenericAPIServer.DiscoveryGroupManager
		}
		delegateAPIServer = server.GenericAPIServer
	}

	return delegateAPIServer, nil
}
