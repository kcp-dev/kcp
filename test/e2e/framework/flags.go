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

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
)

var testConfig = struct {
	kcpKubeconfig       string
	shardKubeconfigs    map[string]string
	useDefaultKCPServer bool
	suites              string
}{}

func complete() {
	if testConfig.useDefaultKCPServer && len(testConfig.kcpKubeconfig) > 0 {
		panic(errors.New("only one of --use-default-kcp-server and --kcp-kubeconfig should be set"))
	}
	if testConfig.useDefaultKCPServer {
		testConfig.kcpKubeconfig = filepath.Join(kcptestinghelpers.RepositoryDir(), ".kcp", "admin.kubeconfig")
	}
	if len(testConfig.kcpKubeconfig) > 0 && len(testConfig.shardKubeconfigs) == 0 {
		testConfig.shardKubeconfigs = map[string]string{corev1alpha1.RootShard: testConfig.kcpKubeconfig}
	}
}

func init() {
	klog.InitFlags(flag.CommandLine)
	if err := flag.Lookup("v").Value.Set("4"); err != nil {
		panic(err)
	}

	flag.StringVar(&testConfig.kcpKubeconfig, "kcp-kubeconfig", "", "Path to the kubeconfig for a kcp server.")
	flag.Var(cliflag.NewMapStringString(&testConfig.shardKubeconfigs), "shard-kubeconfigs", "Paths to the kubeconfigs for a kcp shard server in the format <shard-name>=<kubeconfig-path>. If unset, kcp-kubeconfig is used.")
	flag.BoolVar(&testConfig.useDefaultKCPServer, "use-default-kcp-server", false, "Whether to use server configuration from .kcp/admin.kubeconfig.")
	flag.StringVar(&testConfig.suites, "suites", "control-plane,cli", "A comma-delimited list of suites to run.")

	kcptesting.InitExternalServer(func() (string, map[string]string) {
		complete()
		return testConfig.kcpKubeconfig, testConfig.shardKubeconfigs
	})
}
