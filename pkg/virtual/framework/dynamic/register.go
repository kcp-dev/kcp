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

package dynamic

import (
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
)

// Register builds and returns a DynamicAPIServer which will serve APIs whose serving information is provided by an APISetRetriever.
// The APISetRetriever is returned by the virtual workspace BootstrapAPISetManagement function.
func (vw *DynamicVirtualWorkspace) Register(vwName string, rootAPIServerConfig genericapiserver.CompletedConfig, delegateAPIServer genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	apiSetRetriever, err := vw.BootstrapAPISetManagement(rootAPIServerConfig)
	if err != nil {
		return nil, err
	}

	cfg := &apiserver.DynamicAPIServerConfig{
		GenericConfig: &genericapiserver.RecommendedConfig{Config: *rootAPIServerConfig.Config, SharedInformerFactory: rootAPIServerConfig.SharedInformerFactory},
		ExtraConfig: apiserver.DynamicAPIServerExtraConfig{
			APISetRetriever: apiSetRetriever,
		},
	}

	// We don't want any poststart hooks at the level of a DynamicAPIServer.
	// In the current design, PostStartHooks are only added at the top level RootAPIServer.
	// So let's drop the PostStartHooks from the DynamicAPIServerConfig since they are simply copied
	// from the RootAPIServerConfig
	cfg.GenericConfig.PostStartHooks = map[string]genericapiserver.PostStartHookConfigEntry{}
	config := cfg.Complete()

	server, err := config.New(vwName, delegateAPIServer)
	if err != nil {
		return nil, err
	}

	delegateAPIServer = server.GenericAPIServer

	return delegateAPIServer, nil
}
