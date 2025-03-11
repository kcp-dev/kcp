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

package framework

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	frameworkhelpers "github.com/kcp-dev/kcp/test/e2e/framework/helpers"
	frameworkserver "github.com/kcp-dev/kcp/test/e2e/framework/server"
)

// TestServerArgs returns the set of kcp args used to start a test
// server using the token auth file from the working tree.
func TestServerArgs() []string {
	return append(
		TestServerArgsWithTokenAuthFile("test/e2e/framework/auth-tokens.csv"),
		TestServerWithAuditPolicyFile("test/e2e/framework/audit-policy.yaml")...,
	)
}

// TestServerWithAuditPolicyFile returns the set of kcp args used to
// start a test server with the given audit policy file.
func TestServerWithAuditPolicyFile(auditPolicyFile string) []string {
	return []string{
		"--audit-policy-file", auditPolicyFile,
	}
}

// TestServerArgsWithTokenAuthFile returns the set of kcp args used to
// start a test server with the given token auth file.
func TestServerArgsWithTokenAuthFile(tokenAuthFile string) []string {
	return []string{
		"-v=4",
		"--token-auth-file", tokenAuthFile,
	}
}

// PrivateKcpServer returns a new kcp server fixture managing a new
// server process that is not intended to be shared between tests.
func PrivateKcpServer(t *testing.T, options ...frameworkserver.Option) frameworkserver.RunningServer {
	t.Helper()

	serverName := "main"

	cfg := &frameworkserver.Config{Name: serverName}
	for _, opt := range options {
		cfg = opt(cfg)
	}

	auditPolicyArg := false
	for _, arg := range cfg.Args {
		if arg == "--audit-policy-file" {
			auditPolicyArg = true
		}
	}
	// Default --audit-policy-file or we get no audit info for CI debugging
	if !auditPolicyArg {
		cfg.Args = append(cfg.Args, TestServerWithAuditPolicyFile(WriteEmbedFile(t, "audit-policy.yaml"))...)
	}

	if len(cfg.ArtifactDir) == 0 || len(cfg.DataDir) == 0 {
		artifactDir, dataDir, err := frameworkserver.ScratchDirs(t)
		require.NoError(t, err, "failed to create scratch dirs: %v", err)
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
	}

	f := frameworkserver.NewFixture(t, *cfg)
	return f[serverName]
}

// SharedKcpServer returns a kcp server fixture intended to be shared
// between tests. A persistent server will be configured if
// `--kcp-kubeconfig` or `--use-default-kcp-server` is supplied to the test
// runner. Otherwise a test-managed server will be started. Only tests
// that are known to be hermetic are compatible with shared fixture.
func SharedKcpServer(t *testing.T) frameworkserver.RunningServer {
	t.Helper()

	serverName := "shared"
	kubeconfig := TestConfig.KCPKubeconfig()
	if len(kubeconfig) > 0 {
		// Use a persistent server

		t.Logf("shared kcp server will target configuration %q", kubeconfig)
		s, err := frameworkserver.NewExternalKCPServer(serverName, kubeconfig, TestConfig.ShardKubeconfig(), filepath.Join(frameworkhelpers.RepositoryDir(), ".kcp"))
		require.NoError(t, err, "failed to create persistent server fixture")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		t.Cleanup(cancel)
		err = frameworkserver.WaitForReady(ctx, t, s.RootShardSystemMasterBaseConfig(t), true)
		require.NoError(t, err, "error waiting for readiness")

		return s
	}

	// Use a test-provisioned server
	//
	// TODO(marun) Enable non-persistent fixture to be shared across
	// tests. This will likely require composing tests into a suite that
	// initializes the shared fixture before tests that rely on the
	// fixture.

	artifactDir, dataDir, err := frameworkserver.ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs: %v", err)

	args := TestServerArgsWithTokenAuthFile(WriteTokenAuthFile(t))
	args = append(args, TestServerWithAuditPolicyFile(WriteEmbedFile(t, "audit-policy.yaml"))...)
	clientCADir, clientCAFile := createClientCA(t)
	args = append(args, "--client-ca-file", clientCAFile) //nolint:gocritic // no.
	args = append(args, "--feature-gates=WorkspaceMounts=true")

	f := frameworkserver.NewFixture(t, frameworkserver.Config{
		Name:        serverName,
		Args:        args,
		ArtifactDir: artifactDir,
		ClientCADir: clientCADir,
		DataDir:     dataDir,
	})
	return f[serverName]
}

func createClientCA(t *testing.T) (string, string) {
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
