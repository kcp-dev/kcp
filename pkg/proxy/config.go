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

package proxy

import (
	"context"
	"fmt"
	"net/http"

	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
	bootstrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
)

type Config struct {
	Options *proxyoptions.Options

	ExtraConfig
}

type completedConfig struct {
	Options *proxyoptions.Options

	ExtraConfig
}

type ExtraConfig struct {
	// resolveIdenties is to be called on server start until it succeeds. It injects the kcp
	// resource identities into the rest.Config used by the client. Only after it succeeds,
	// the clients can wildcard-list/watch most kcp resources.
	ResolveIdentities func(ctx context.Context) error
	RootShardConfig   *rest.Config
	ShardsConfig      *rest.Config

	AuthenticationInfo    genericapiserver.AuthenticationInfo
	ServingInfo           *genericapiserver.SecureServingInfo
	AdditionalAuthEnabled bool
}

type CompletedConfig struct {
	// embed a private pointer that cannot be instantiated outside this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	return CompletedConfig{&completedConfig{
		Options:     c.Options,
		ExtraConfig: c.ExtraConfig,
	}}, nil
}

// NewConfig returns a new Config for the given options.
func NewConfig(opts *proxyoptions.Options) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	// get root API identities
	nonIdentityRootConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.RootKubeconfig}, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load root kubeconfig: %w", err)
	}

	var kcpShardIdentityRoundTripper func(rt http.RoundTripper) http.RoundTripper
	kcpShardIdentityRoundTripper, c.ResolveIdentities = bootstrap.NewWildcardIdentitiesWrappingRoundTripper(bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, nonIdentityRootConfig, nil)
	c.RootShardConfig = rest.CopyConfig(nonIdentityRootConfig)
	c.RootShardConfig.Wrap(kcpShardIdentityRoundTripper)

	var loopbackClientConfig *rest.Config
	if err := c.Options.SecureServing.ApplyTo(&c.ServingInfo, &loopbackClientConfig); err != nil {
		return nil, err
	}
	if err := c.Options.Authentication.ApplyTo(&c.AuthenticationInfo, c.ServingInfo, c.RootShardConfig); err != nil {
		return nil, err
	}

	c.ShardsConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.ShardsKubeconfig},
		// We override the Server here so that the user doesn't have to specify unused server value
		// The Server must have HTTPS scheme otherwise CA won't be loaded (see IsConfigTransportTLS method)
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: "https://fakeserver.io"}}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load shard kubeconfig: %w", err)
	}
	c.ShardsConfig.Wrap(kcpShardIdentityRoundTripper)

	c.AdditionalAuthEnabled = c.Options.Authentication.AdditionalAuthEnabled()

	return c, nil
}
