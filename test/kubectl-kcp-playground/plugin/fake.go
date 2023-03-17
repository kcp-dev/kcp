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

package plugin

import (
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type fakePCluster struct {
	framework.RunningServer
	name string
}

var _ RunningPCluster = &fakePCluster{}

func (f fakePCluster) Name() string {
	return f.name
}

func (f fakePCluster) Type() PClusterType {
	return FakePClusterType
}

func (f fakePCluster) KubeconfigPath() string {
	return f.RunningServer.KubeconfigPath()
}

func (f fakePCluster) RawConfig() (clientcmdapi.Config, error) {
	return f.RunningServer.RawConfig()
}
