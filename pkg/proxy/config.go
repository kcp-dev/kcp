/*
Copyright 2022 The kcp Authors.

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
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
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
	// CoreIdentityHandler feeds the identities of the core APIs, which the
	// clients built from ShardsConfig inject into their wildcard requests,
	// from the APIIdentity registry that the Admin workspace of the peers
	// serves. Register it on an informer of those entries.
	CoreIdentityHandler cache.ResourceEventHandler
	// CoreIdentitiesReady fails while the identity of a core API is still
	// unknown. It is a readiness check: requests needing an identity fail
	// until it arrives, nothing blocks startup on it.
	CoreIdentitiesReady func() error
	// RootShardConfig points at the root shard and is set only when
	// --root-kubeconfig is given. Service account authentication still needs
	// it.
	RootShardConfig *rest.Config
	ShardsConfig    *rest.Config
	// PeersConfig points at the Admin workspace (/services/admin) of the
	// peer shards with round-robin and per-request failover, and is the only
	// source for Shard discovery. When no peers are configured, the root
	// kubeconfig's server acts as the seed peer.
	PeersConfig *rest.Config
	// Peers is the failover set behind PeersConfig: the configured seed
	// peers plus every discovered shard, fed at runtime from the shard
	// informer so discovery survives seed peers going away.
	Peers *Peers

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
func NewConfig(ctx context.Context, opts *proxyoptions.Options) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	// The identities of the core APIs come from the APIIdentity registry,
	// read through the Admin workspace of the peers; the root shard is not
	// asked for them.
	var kcpShardIdentityRoundTripper func(rt http.RoundTripper) http.RoundTripper
	kcpShardIdentityRoundTripper, c.CoreIdentityHandler, c.CoreIdentitiesReady = bootstrap.NewRegistryWildcardIdentities(bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames)

	//nolint:staticcheck // the deprecated root kubeconfig is still needed for service account authentication and as the default seed peer.
	if c.Options.RootKubeconfig != "" {
		//nolint:staticcheck // see above.
		rootConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.RootKubeconfig}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load root kubeconfig: %w", err)
		}
		c.RootShardConfig = rest.CopyConfig(rootConfig)
		c.RootShardConfig.Wrap(kcpShardIdentityRoundTripper)
	}

	var loopbackClientConfig *rest.Config
	if err := c.Options.SecureServing.ApplyTo(&c.ServingInfo, &loopbackClientConfig); err != nil {
		return nil, err
	}
	if err := c.Options.Authentication.ApplyTo(ctx, &c.AuthenticationInfo, c.ServingInfo, c.RootShardConfig); err != nil {
		return nil, err
	}

	var err error
	c.ShardsConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.ShardsKubeconfig},
		// We override the Server here so that the user doesn't have to specify unused server value
		// The Server must have HTTPS scheme otherwise CA won't be loaded (see IsConfigTransportTLS method)
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: "https://kcp.io/fake"}}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load shard kubeconfig: %w", err)
	}
	c.ShardsConfig.Wrap(kcpShardIdentityRoundTripper)

	// Shard discovery and the core identities always go through the Admin
	// workspace of the peers; the root shard is never asked. Without
	// configured peers, the root kubeconfig's server is the seed peer.
	peerKubeconfigs := c.Options.ShardsPeerKubeconfigs
	if len(peerKubeconfigs) == 0 {
		//nolint:staticcheck // the deprecated root kubeconfig doubles as the default seed peer for deployments that predate --shard-peer-kubeconfig.
		if c.Options.RootKubeconfig == "" {
			return nil, fmt.Errorf("--shard-peer-kubeconfig is required")
		}
		//nolint:staticcheck // see above.
		peerKubeconfigs = []string{c.Options.RootKubeconfig}
	}
	c.PeersConfig, c.Peers, err = NewPeersConfig(peerKubeconfigs)
	if err != nil {
		return nil, err
	}

	c.AdditionalAuthEnabled = c.Options.Authentication.AdditionalAuthEnabled()

	return c, nil
}
