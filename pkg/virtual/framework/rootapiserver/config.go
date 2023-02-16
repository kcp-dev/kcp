/*
Copyright 2023 The KCP Authors.

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

package rootapiserver

import (
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
)

type NamedVirtualWorkspace struct {
	Name string
	framework.VirtualWorkspace
}

type Config struct {
	Generic *genericapiserver.RecommendedConfig
	Extra   ExtraConfig
}

type InformerStart func(stopCh <-chan struct{})

type ExtraConfig struct {
	// we phrase it like this so we can build the post-start-hook, but no one can take more indirect dependencies on informers
	informerStart func(stopCh <-chan struct{})

	VirtualWorkspaces []NamedVirtualWorkspace
}

type completedConfig struct {
	Generic genericapiserver.CompletedConfig
	Extra   *ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() completedConfig {
	cfg := completedConfig{
		c.Generic.Complete(),
		&c.Extra,
	}

	return cfg
}

func (c *completedConfig) WithOpenAPIAggregationController(delegatedAPIServer *genericapiserver.GenericAPIServer) error {
	return nil
}

func NewConfig(recommendedConfig *genericapiserver.RecommendedConfig, informerStarts []InformerStart) (*Config, error) {
	// TODO: genericConfig.ExternalAddress = ... allow a command line flag or it to be overridden by a top-level multiroot apiServer

	// Loopback is not wired for now, since virtual workspaces are expected to delegate to
	// some APIServer.
	// The RootAPISrver is just a proxy to the various virtual workspaces.
	// We might consider a giving a special meaning to a global loopback config, in the future
	// but that's not the case for now.
	recommendedConfig.Config.LoopbackClientConfig = &rest.Config{
		Host: "loopback-config-not-wired-for-now",
	}

	ret := &Config{
		Generic: recommendedConfig,
		Extra: ExtraConfig{
			informerStart: func(stopCh <-chan struct{}) {
				for _, informerStart := range informerStarts {
					informerStart(stopCh)
				}
			},
		},
	}

	return ret, nil
}
