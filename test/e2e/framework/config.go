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
	syncerImage                   string
	kcpTestImage                  string
	pclusterKubeconfig            string
	kcpKubeconfig, rootKubeconfig string
	rootShardAdminContext         string
	useDefaultKCPServer           bool
}

var TestConfig *testConfig

func (c *testConfig) SyncerImage() string {
	return c.syncerImage
}

func (c *testConfig) KCPTestImage() string {
	return c.kcpTestImage
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

func (c *testConfig) RootKubeconfig() string {
	if c.useDefaultKCPServer && len(c.rootKubeconfig) > 0 {
		panic(errors.New("Only one of --use-default-kcp-server and --root-kubeconfig should be set."))
	}

	return c.rootKubeconfig
}

func (c *testConfig) RootShardAdminContext() string {
	return c.rootShardAdminContext
}

func init() {
	TestConfig = &testConfig{}
	registerFlags(TestConfig)
	// The testing package will call flags.Parse()
}

func registerFlags(c *testConfig) {
	flag.StringVar(&c.kcpKubeconfig, "kcp-kubeconfig", "", "Path to the kubeconfig for a kcp server.")
	flag.StringVar(&c.rootKubeconfig, "root-kubeconfig", "", "Path to the kubeconfig for a root kcp shard. If not set, the kcp-kubeconfig is used.")
	flag.StringVar(&c.rootShardAdminContext, "root-shard-admin-context", "base", "The context in the root-kubeconfig for a system:masters shard admin.")
	flag.StringVar(&c.pclusterKubeconfig, "pcluster-kubeconfig", "", "Path to the kubeconfig for a kubernetes cluster to sync to. Requires --syncer-image.")
	flag.StringVar(&c.syncerImage, "syncer-image", "", "The syncer image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.StringVar(&c.kcpTestImage, "kcp-test-image", "", "The test image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.BoolVar(&c.useDefaultKCPServer, "use-default-kcp-server", false, "Whether to use server configuration from .kcp/admin.kubeconfig.")
}
