/*
Copyright 2021 The KCP Authors.

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
	"context"
	"embed"
	"path/filepath"

	"github.com/stretchr/testify/require"

	utilfeature "k8s.io/apiserver/pkg/util/feature"

	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"
)

//go:embed *.yaml
var fs embed.FS

// PrivateKcpServer returns a new kcp server fixture managing a new
// server process that is not intended to be shared between tests.
func PrivateKcpServer(t TestingT, options ...kcptestingserver.Option) kcptestingserver.RunningServer {
	t.Helper()

	cfg := &kcptestingserver.Config{
		Name:        "main",
		BindAddress: "127.0.0.1",
		Features:    utilfeature.DefaultMutableFeatureGate.DeepCopy(),
	}
	for _, opt := range options {
		opt(cfg)
	}

	serverName := cfg.Name

	auditPolicyArg := false
	for _, arg := range cfg.Args {
		if arg == "--audit-policy-file" {
			auditPolicyArg = true
		}
	}
	// Default --audit-policy-file, or we get no audit info for CI debugging
	if !auditPolicyArg {
		cfg.Args = append(cfg.Args, "--audit-policy-file", copyEmbeddedToTempDir(t, fs, "audit-policy.yaml"))
	}

	if len(cfg.ArtifactDir) == 0 || len(cfg.DataDir) == 0 {
		artifactDir, dataDir, err := kcptestingserver.ScratchDirs(t)
		require.NoError(t, err, "failed to create scratch dirs: %v", err)
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
	}

	f := kcptestingserver.NewFixture(t, *cfg)
	return f[serverName]
}

// SharedKcpServer returns a kcp server fixture intended to be shared
// between tests. A persistent server will be configured if
// `--kcp-kubeconfig` or `--use-default-kcp-server` is supplied to the test
// runner. Otherwise a test-managed server will be started. Only tests
// that are known to be hermetic are compatible with shared fixture.
func SharedKcpServer(t TestingT) kcptestingserver.RunningServer {
	t.Helper()

	setupExternal()
	if len(externalConfig.kubeconfigPath) > 0 {
		// Use a pre-existing external server

		t.Logf("Shared kcp server will target configuration %q", externalConfig.kubeconfigPath)
		s, err := kcptestingserver.NewExternalKCPServer(sharedConfig.Name, externalConfig.kubeconfigPath, externalConfig.shardKubeconfigPaths, filepath.Dir(KubeconfigPath()))
		require.NoError(t, err, "failed to create persistent server fixture")

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		rootCfg := s.RootShardSystemMasterBaseConfig(t)
		t.Logf("Waiting for readiness for server at %s", rootCfg.Host)
		err = kcptestingserver.WaitForReady(ctx, rootCfg)
		require.NoError(t, err, "external server is not ready")

		kcptestingserver.MonitorEndpoints(t, rootCfg, "/livez", "/readyz")

		return s
	}

	c := sharedConfig

	if c.ArtifactDir == "" || c.DataDir == "" {
		artifacts, data, err := kcptestingserver.ScratchDirs(t)
		require.NoError(t, err, "failed to create scratch dirs: %v", err)
		if c.ArtifactDir == "" {
			c.ArtifactDir = artifacts
		}
		if c.DataDir == "" {
			c.DataDir = data
		}
	}

	args := append([]string{}, c.Args...)
	args = append(args, "--audit-policy-file", copyEmbeddedToTempDir(t, fs, "audit-policy.yaml"))

	if c.ClientCADir == "" {
		var clientCAFile string
		c.ClientCADir, clientCAFile = createClientCA(t)
		args = append(args, "--client-ca-file", clientCAFile)
	}

	c.Args = args
	f := kcptestingserver.NewFixture(t, c)
	return f[c.Name]
}

func createClientCA(t TestingT) (string, string) {
	clientCADir := t.TempDir()
	_, err := crypto.MakeSelfSignedCA(
		filepath.Join(clientCADir, "client-ca.crt"),
		filepath.Join(clientCADir, "client-ca.key"),
		filepath.Join(clientCADir, "client-ca-serial.txt"),
		"kcp-client-ca",
		365,
	)
	require.NoError(t, err)
	return clientCADir, filepath.Join(clientCADir, "client-ca.crt")
}
