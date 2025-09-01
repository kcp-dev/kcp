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

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type testCli struct {
	server         kcptestingserver.RunningServer
	wsName         string
	kubeconfigPath string
}

func newTestCli(t *testing.T) *testCli {
	t.Helper()

	tc := &testCli{}
	tc.server = kcptesting.SharedKcpServer(t)
	tc.wsName = safeResourceName(t.Name())
	tc.kubeconfigPath = writeKubeconfig(t, tc.server)

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		t.Logf("Test failed, dumping kubeconfig for debugging:\n%s", func() string {
			data, err := os.ReadFile(tc.kubeconfigPath)
			if err != nil {
				return err.Error()
			}
			return strings.TrimSpace(string(data))
		}())
	})

	return tc
}

func safeResourceName(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return strings.TrimSpace(s)
}

func (tc *testCli) runKubectl(t *testing.T, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	return framework.RunKubectl(t, tc.kubeconfigPath, args...)
}

func (tc *testCli) runPlugin(t *testing.T, plugin string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	return framework.RunKcpCliPlugin(t, tc.kubeconfigPath, plugin, args...)
}

func writeKubeconfig(t *testing.T, server kcptestingserver.RunningServer) string {
	t.Helper()

	rawConfig, err := server.RawConfig()
	require.NoError(t, err)

	tmpdir := t.TempDir()
	kubeconfigPath := filepath.Join(tmpdir, "kubeconfig.yaml")
	err = clientcmd.WriteToFile(rawConfig, kubeconfigPath)
	require.NoError(t, err)

	return kubeconfigPath
}

func (tc *testCli) readKubeconfig(t *testing.T) clientcmdapi.Config {
	t.Helper()
	config, err := clientcmd.LoadFromFile(tc.kubeconfigPath)
	require.NoError(t, err)
	return *config
}

func (tc *testCli) workspaceShouldExist(t *testing.T, wsName string) {
	t.Helper()

	clientset, err := kcpclientset.NewForConfig(tc.server.BaseConfig(t))
	if err != nil {
		t.Error("failed to create clientset:", err)
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := clientset.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(t.Context(), wsName, v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %q not found", wsName)
}
