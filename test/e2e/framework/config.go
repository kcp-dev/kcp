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

package framework

import (
	"errors"
	"flag"
	"path/filepath"
)

type testConfig struct {
	syncerImage              string
	inClusterConfigTestImage string
	pclusterKubeconfig       string
	kcpKubeconfig            string
	useDefaultKCPServer      bool
}

var TestConfig *testConfig

func (c *testConfig) SyncerImage() string {
	return c.syncerImage
}

func (c *testConfig) InClusterConfigTestImage() string {
	return c.inClusterConfigTestImage
}

func (c *testConfig) PClusterKubeconfig() string {
	return c.pclusterKubeconfig
}

func (c *testConfig) KCPKubeconfig() string {
	// TODO(marun) How to validate before use given that the testing package is calling flags.Parse()?
	if c.useDefaultKCPServer && len(c.kcpKubeconfig) > 0 {
		panic(errors.New("Only one of --use-default-kcp-server and --kcp-kubeconfig should be set."))
	}

	if c.useDefaultKCPServer {
		return filepath.Join(RepositoryDir(), ".kcp", "admin.kubeconfig")
	} else {
		return c.kcpKubeconfig
	}
}

func init() {
	TestConfig = &testConfig{}
	registerFlags(TestConfig)
	// The testing package will call flags.Parse()
}

func registerFlags(c *testConfig) {
	flag.StringVar(&c.kcpKubeconfig, "kcp-kubeconfig", "", "Path to the kubeconfig for a kcp server.")
	flag.StringVar(&c.pclusterKubeconfig, "pcluster-kubeconfig", "", "Path to the kubeconfig for a kubernetes cluster to sync to. Requires --syncer-image.")
	flag.StringVar(&c.syncerImage, "syncer-image", "", "The syncer image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.StringVar(&c.inClusterConfigTestImage, "icc-test-image", "", "The in-cluster configuration test image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.BoolVar(&c.useDefaultKCPServer, "use-default-kcp-server", false, "Whether to use server configuration from .kcp/admin.kubeconfig.")
}
