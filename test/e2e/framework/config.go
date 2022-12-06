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
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
)

func init() {
	klog.InitFlags(flag.CommandLine)
	if err := flag.Lookup("v").Value.Set("4"); err != nil {
		panic(err)
	}
}

type testConfig struct {
	syncerImage                        string
	kcpTestImage                       string
	pclusterKubeconfig                 string
	kcpKubeconfig, rootShardKubeconfig string
	useDefaultKCPServer                bool
	suites                             string
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
		panic(errors.New("only one of --use-default-kcp-server and --kcp-kubeconfig should be set"))
	}

	if c.useDefaultKCPServer {
		return filepath.Join(RepositoryDir(), ".kcp", "admin.kubeconfig")
	} else {
		return c.kcpKubeconfig
	}
}

func (c *testConfig) RootShardKubeconfig() string {
	if c.rootShardKubeconfig == "" {
		return c.KCPKubeconfig()
	}
	return c.rootShardKubeconfig
}

func (c *testConfig) Suites() []string {
	return strings.Split(c.suites, ",")
}

func init() {
	TestConfig = &testConfig{}
	registerFlags(TestConfig)
	// The testing package will call flags.Parse()
}

func registerFlags(c *testConfig) {
	flag.StringVar(&c.kcpKubeconfig, "kcp-kubeconfig", "", "Path to the kubeconfig for a kcp server.")
	flag.StringVar(&c.rootShardKubeconfig, "root-shard-kubeconfig", "", "Path to the kubeconfig for a kcp shard server. If unset, kcp-kubeconfig is used.")
	flag.StringVar(&c.pclusterKubeconfig, "pcluster-kubeconfig", "", "Path to the kubeconfig for a kubernetes cluster to sync to. Requires --syncer-image.")
	flag.StringVar(&c.syncerImage, "syncer-image", "", "The syncer image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.StringVar(&c.kcpTestImage, "kcp-test-image", "", "The test image to use with the pcluster. Requires --pcluster-kubeconfig")
	flag.BoolVar(&c.useDefaultKCPServer, "use-default-kcp-server", false, "Whether to use server configuration from .kcp/admin.kubeconfig.")
	flag.StringVar(&c.suites, "suites", "control-plane,transparent-multi-cluster,transparent-multi-cluster:requires-kind", "A comma-delimited list of suites to run.")
}

// WriteLogicalClusterConfig creates a logical cluster config for the given config and
// cluster name and writes it to the test's artifact path. Useful for configuring the
// workspace plugin with --kubeconfig.
func WriteLogicalClusterConfig(t *testing.T, rawConfig clientcmdapi.Config, contextName string, clusterName logicalcluster.Path) (clientcmd.ClientConfig, string) {
	logicalRawConfig := LogicalClusterRawConfig(rawConfig, clusterName, contextName)
	artifactDir, _, err := ScratchDirs(t)
	require.NoError(t, err)
	pathSafeClusterName := strings.ReplaceAll(clusterName.String(), ":", "_")
	kubeconfigPath := filepath.Join(artifactDir, fmt.Sprintf("%s.kubeconfig", pathSafeClusterName))
	err = clientcmd.WriteToFile(logicalRawConfig, kubeconfigPath)
	require.NoError(t, err)
	logicalConfig := clientcmd.NewNonInteractiveClientConfig(logicalRawConfig, logicalRawConfig.CurrentContext, &clientcmd.ConfigOverrides{}, nil)
	return logicalConfig, kubeconfigPath
}

// ShardConfig returns a rest config that talk directly to the given shard.
func ShardConfig(t *testing.T, kcpClusterClient kcpclientset.ClusterInterface, shardName string, cfg *rest.Config) *rest.Config {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	shard, err := kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster.Path()).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, shardName, metav1.GetOptions{})
	require.NoError(t, err)

	shardCfg := rest.CopyConfig(cfg)
	shardCfg.Host = shard.Spec.BaseURL

	return shardCfg
}
