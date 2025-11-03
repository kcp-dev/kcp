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

package testing

import (
	"sync"

	utilfeature "k8s.io/apiserver/pkg/util/feature"

	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

// SharedKcpOption is a function that can be used to configure the shared kcp
// server.
type SharedKcpOption func()

var (
	sharedConfig = kcptestingserver.Config{
		Name:        "shared",
		BindAddress: "127.0.0.1",
		Features:    utilfeature.DefaultMutableFeatureGate.DeepCopy(),
	}
	externalConfig = struct {
		kubeconfigPath       string
		shardKubeconfigPaths map[string]string
	}{}

	externalSetupOnce sync.Once
	externalSetupFn   func() (kubeconfigPath string, shardKubeconfigPaths map[string]string)
)

// InitSharedKcpServer initializes a shared kcp server fixture. It must be
// called before SharedKcpServer is called.
func InitSharedKcpServer(opts ...kcptestingserver.Option) {
	for _, opt := range opts {
		opt(&sharedConfig)
	}
}

// InitExternalServer configures a potentially pre-existing shared external kcp
// server. It must be called before SharedKcpServer is called. The shard
// kubeconfigs are optional, but the kubeconfigPath must be provided.
func InitExternalServer(fn func() (kubeconfigPath string, shardKubeconfigPaths map[string]string)) {
	externalSetupFn = fn
}

func setupExternal() {
	externalSetupOnce.Do(func() {
		if externalSetupFn != nil {
			externalConfig.kubeconfigPath, externalConfig.shardKubeconfigPaths = externalSetupFn()
		}
	})
}

func KubeconfigPath() string {
	return externalConfig.kubeconfigPath
}

func ShardKubeconfigPaths(shard string) string {
	return externalConfig.shardKubeconfigPaths[shard]
}
