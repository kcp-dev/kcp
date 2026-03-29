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

package framework

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/client-go/tools/clientcmd"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

type TestCli struct {
	Server         kcptestingserver.RunningServer
	KubeconfigPath string
}

func NewCli(t *testing.T) *TestCli {
	t.Helper()

	tc := &TestCli{}
	tc.Server = kcptesting.SharedKcpServer(t)
	tc.KubeconfigPath = writeKubeconfig(t, tc.Server)

	return tc
}

func (tc *TestCli) RunPlugin(t *testing.T, plugin string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	return RunKcpCliPlugin(t, tc.KubeconfigPath, plugin, args)
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
