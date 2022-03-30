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
	kubeconfig       string
	useDefaultServer bool
}

var TestConfig *testConfig

func (c *testConfig) Kubeconfig() string {
	// TODO(marun) How to validate before use given that the testing package is calling flags.Parse()?
	if c.useDefaultServer && len(c.kubeconfig) > 0 {
		panic(errors.New("Only one of --use-default-server and --kubeconfig should be set."))
	}

	if c.useDefaultServer {
		return filepath.Join(RepositoryDir(), ".kcp", "admin.kubeconfig")
	} else {
		return c.kubeconfig
	}
}

func init() {
	TestConfig = &testConfig{}
	registerFlags(TestConfig)
	// The testing package will call flags.Parse()
}

func registerFlags(c *testConfig) {
	flag.StringVar(&c.kubeconfig, "kubeconfig", "", "Path to kubeconfig for a kcp server.")
	flag.BoolVar(&c.useDefaultServer, "use-default-server", false, "Whether to use server configuration from .kcp/admin.kubeconfig.")
}
