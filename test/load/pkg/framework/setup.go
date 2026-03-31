/*
Copyright 2026 The kcp Authors.

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

// Setup is a centralized loadtest setup that reads configuration from
// environment variables. Pass in the required capabilities and Require
// will validate that the corresponding env vars are set and load the
// kubeconfigs into *rest.Config.
//
// It is using env vars instead of flags, as flags would be a huge pain for two reasons:
//	 1. We would need a custom TestMain() in each package, which wants to use setup
//	 2. We could not test using go test ./..., as flags would be shared across all tests,
//	 leading to failures for test, which don't use setup

package framework

import (
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Config holds the resolved configuration based on the capabilities
// requested in Require. Only fields corresponding to requested capabilities
// are guaranteed to be populated.
type Config struct {
	// FrontProxyKubeconfig is the rest.Config of a kcp front-proxy
	FrontProxyKubeconfig *rest.Config

	// ShardKubeconfig is the rest.Config of a kcp shard
	ShardKubeconfig *rest.Config
}

// Capability identifies a requirement that a load test has on the test
// environment. Pass one or more capabilities to Require() to declare which
// environment variables must be set.
type Capability string

var (
	// KCPFrontProxyKubeconfig declares that the test requires the
	// FRONTPROXY_KUBECONFIG env var to be set.
	KCPFrontProxyKubeconfig Capability = "FRONTPROXY_KUBECONFIG"

	// KCPShardKubeconfig declares that the test requires the
	// SHARD_KUBECONFIG env var to be set.
	KCPShardKubeconfig Capability = "SHARD_KUBECONFIG"
)

// Require validates that all environment variables required by the given
// capabilities have been provided and returns a Config populated with the
// corresponding values.
// It calls t.Fatal if any required env var is missing or a kubeconfig
// cannot be loaded.
func Require(t *testing.T, caps ...Capability) *Config {
	t.Helper()

	cfg := &Config{}

	if slices.Contains(caps, KCPFrontProxyKubeconfig) {
		cfg.FrontProxyKubeconfig = loadKubeconfig(t, os.Getenv(string(KCPFrontProxyKubeconfig)))
		assert.NotEmptyf(t, cfg.FrontProxyKubeconfig, "%s env var must be set", KCPFrontProxyKubeconfig)
	}

	if slices.Contains(caps, KCPShardKubeconfig) {
		cfg.ShardKubeconfig = loadKubeconfig(t, os.Getenv(string(KCPShardKubeconfig)))
		assert.NotEmptyf(t, cfg.ShardKubeconfig, "%s env var must be set", KCPShardKubeconfig)
	}

	require.False(t, t.Failed(), "missing required components and/or settings")

	return cfg
}

func loadKubeconfig(t *testing.T, kubeconfigPath string) *rest.Config {
	t.Helper()

	if kubeconfigPath == "" {
		return nil
	}

	rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	require.NoError(t, err, "failed to load kubeconfig from %s", kubeconfigPath)

	restConfig, err := clientcmd.NewNonInteractiveClientConfig(*rawConfig, rawConfig.CurrentContext, nil, nil).ClientConfig()
	require.NoError(t, err, "failed to create rest.Config from %s", kubeconfigPath)

	return restConfig
}
