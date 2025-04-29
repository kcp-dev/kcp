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

package server

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type RunningServer interface {
	Name() string
	KubeconfigPath() string
	RawConfig() (clientcmdapi.Config, error)
	BaseConfig(t TestingT) *rest.Config
	RootShardSystemMasterBaseConfig(t TestingT) *rest.Config
	ShardSystemMasterBaseConfig(t TestingT, shard string) *rest.Config
	ShardNames() []string
	Artifact(t TestingT, producer func() (runtime.Object, error))
	ClientCAUserConfig(t TestingT, config *rest.Config, name string, groups ...string) *rest.Config
	CADirectory() string
	// Stop signals the server to shutdown and waits until it finishes.
	// Stop is a noop for external servers.
	Stop()
	// Stopped returns true if the server has ran and stopped.
	// Stopped is a noop for external servers.
	Stopped() bool
}
