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

package logicalclustermigration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

// privateMigrationServer starts two private shards and a front proxy. The root
// shard also hosts their shared cache. All ports, storage, and credentials are
// allocated per test, independently of --kcp-kubeconfig and --shard-kubeconfigs.
func privateMigrationServer(t *testing.T) kcptestingserver.RunningServer {
	t.Helper()

	artifacts, dataDir, err := kcptestingserver.ScratchDirs(t)
	require.NoError(t, err)
	rootPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	destinationPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	proxyPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	rootURL := "https://127.0.0.1:" + rootPort
	destinationURL := "https://127.0.0.1:" + destinationPort
	proxyURL := "https://127.0.0.1:" + proxyPort

	newCA := func(name string) *crypto.CA {
		t.Helper()
		ca, _, err := crypto.EnsureCA(filepath.Join(dataDir, name+".crt"), filepath.Join(dataDir, name+".key"), filepath.Join(dataDir, name+".serial"), name, 1)
		require.NoError(t, err)
		return ca
	}
	servingCA := newCA("serving-ca")
	clientCA := newCA("client-ca")
	requestHeaderCA := newCA("requestheader-ca")
	servingCAFile := filepath.Join(dataDir, "serving-ca.crt")
	clientCAFile := filepath.Join(dataDir, "client-ca.crt")
	requestHeaderCAFile := filepath.Join(dataDir, "requestheader-ca.crt")

	servingCert, err := servingCA.MakeServerCert(sets.New("localhost", "127.0.0.1"), 1)
	require.NoError(t, err)
	certFile, keyFile := filepath.Join(dataDir, "serving.crt"), filepath.Join(dataDir, "serving.key")
	require.NoError(t, servingCert.WriteCertConfigFile(certFile, keyFile))
	adminCert, adminKey := filepath.Join(dataDir, "admin.crt"), filepath.Join(dataDir, "admin.key")
	// Direct shard/cache requests need system:masters; the front proxy strips
	// that group, so requests through it use the kcp administrator group.
	_, err = clientCA.MakeClientCertificate(adminCert, adminKey, &user.DefaultInfo{
		Name: "migration-test-admin", Groups: []string{user.SystemPrivilegedGroup, bootstrap.SystemKcpAdminGroup},
	}, 1)
	require.NoError(t, err)
	proxyCert, proxyKey := filepath.Join(dataDir, "proxy.crt"), filepath.Join(dataDir, "proxy.key")
	_, err = requestHeaderCA.MakeClientCertificate(proxyCert, proxyKey, &user.DefaultInfo{Name: "migration-test-proxy"}, 1)
	require.NoError(t, err)

	writeConfig := func(name, host, clientCert, clientKey string) string {
		t.Helper()
		config := clientcmdapi.Config{
			Clusters:  map[string]*clientcmdapi.Cluster{"server": {Server: host, CertificateAuthority: servingCAFile}},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{"admin": {ClientCertificate: clientCert, ClientKey: clientKey}},
			Contexts: map[string]*clientcmdapi.Context{
				"base":       {Cluster: "server", AuthInfo: "admin"},
				"shard-base": {Cluster: "server", AuthInfo: "admin"},
			},
			CurrentContext: "base",
		}
		path := filepath.Join(dataDir, name+".kubeconfig")
		require.NoError(t, clientcmd.WriteToFile(config, path))
		return path
	}
	rootConfig := writeConfig("root", rootURL, adminCert, adminKey)
	destinationConfig := writeConfig("destination", destinationURL, adminCert, adminKey)
	proxyConfig := writeConfig("proxy", proxyURL, adminCert, adminKey)
	// Migration and workspace controllers use dedicated identities, including
	// the external logical-cluster administrator required by the dump endpoint.
	controllerConfig := func(name, host, group string) string {
		t.Helper()
		cert, key := filepath.Join(dataDir, name+".crt"), filepath.Join(dataDir, name+".key")
		_, err := clientCA.MakeClientCertificate(cert, key, &user.DefaultInfo{Name: name, Groups: []string{group}}, 1)
		require.NoError(t, err)
		return writeConfig(name, host, cert, key)
	}
	logicalClusterAdminConfig := controllerConfig("logical-cluster-admin", rootURL, bootstrap.SystemLogicalClusterAdmin)
	externalLogicalClusterAdminConfig := controllerConfig("external-logical-cluster-admin", proxyURL, bootstrap.SystemExternalLogicalClusterAdmin)

	// Start the proxy first: shard controllers use its endpoint for cross-shard
	// requests. It discovers the private shards once the root becomes ready.
	mappings, err := json.Marshal([]types.PathMapping{
		{
			Path: "/clusters/", Backend: rootURL, BackendServerCA: servingCAFile,
			ProxyClientCert: proxyCert, ProxyClientKey: proxyKey,
		},
		{
			Path: "/services/admin", Backend: rootURL, BackendServerCA: servingCAFile,
			ProxyClientCert: proxyCert, ProxyClientKey: proxyKey,
		},
	})
	require.NoError(t, err)
	mappingFile := filepath.Join(dataDir, "mapping.json")
	require.NoError(t, os.WriteFile(mappingFile, mappings, 0600))
	workdir, command := kcptestingserver.Command("kcp-front-proxy", "migration-test-proxy")
	command = append(command,
		"--bind-address=127.0.0.1", "--secure-port="+proxyPort,
		"--root-directory="+filepath.Join(dataDir, "proxy"),
		"--root-kubeconfig="+rootConfig, "--shard-peer-kubeconfig="+rootConfig,
		"--shards-kubeconfig="+rootConfig, "--mapping-file="+mappingFile,
		"--client-ca-file="+clientCAFile,
		"--tls-cert-file="+certFile, "--tls-private-key-file="+keyFile,
	)
	// Use a separate context so the proxy stays up for workspace cleanup,
	// even after t.Context() has been cancelled.
	proxyCtx, stopProxy := context.WithCancel(context.Background())
	t.Cleanup(stopProxy)
	cmd := exec.CommandContext(proxyCtx, command[0], command[1:]...)
	cmd.Dir = workdir
	// Signal the process group so cleanup also stops the child of go run.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	proxyLog, err := os.Create(filepath.Join(artifacts, "front-proxy.log"))
	require.NoError(t, err)
	cmd.Stdout, cmd.Stderr = proxyLog, proxyLog
	require.NoError(t, cmd.Start())
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- cmd.Wait() }()
	t.Cleanup(func() {
		stopProxy()
		err := <-proxyDone
		if err != nil {
			t.Logf("front proxy exited during cleanup: %v", err)
		}
		require.NoError(t, proxyLog.Close())
	})

	commonArgs := []string{
		"--feature-gates=LogicalClusterMigration=true", "--run-virtual-workspaces=true",
		"--tls-cert-file=" + certFile, "--tls-private-key-file=" + keyFile,
		"--client-ca-file=" + clientCAFile,
		"--requestheader-client-ca-file=" + requestHeaderCAFile,
		"--requestheader-username-headers=X-Remote-User", "--requestheader-group-headers=X-Remote-Group",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-", "--requestheader-allowed-names=migration-test-proxy",
		"--logical-cluster-admin-kubeconfig=" + logicalClusterAdminConfig,
		"--external-logical-cluster-admin-kubeconfig=" + externalLogicalClusterAdminConfig,
		"--shard-external-url=" + proxyURL,
		"--shard-client-cert-file=" + adminCert, "--shard-client-key-file=" + adminKey,
		"--shard-virtual-workspace-ca-file=" + servingCAFile,
	}
	startShard := func(name, port string, extraArgs ...string) {
		t.Helper()
		args := append([]string{}, commonArgs...)
		args = append(args, "--shard-name="+name, "--secure-port="+port)
		args = append(args, extraArgs...)
		kcptesting.PrivateKcpServer(t,
			kcptestingserver.WithScratchDirectories(filepath.Join(artifacts, name), filepath.Join(dataDir, name)),
			kcptestingserver.WithClientCA(dataDir),
			kcptestingserver.WithCustomArguments(args...),
		)
	}
	startShard("root", rootPort)
	startShard("shard-1", destinationPort, "--root-shard-kubeconfig-file="+rootConfig, "--cache-kubeconfig="+rootConfig)
	// Cleanup runs in reverse order: delete workspaces first, then stop the
	// proxy's wildcard watches before shutting down the shards they watch.
	t.Cleanup(stopProxy)

	// This adapter exposes the private proxy and both shard configs to the
	// standard workspace fixtures. Process lifetimes belong to this test above.
	server, err := kcptestingserver.NewExternalKCPServer(t.Name(), proxyConfig, map[string]string{
		"root": rootConfig, "shard-1": destinationConfig,
	}, dataDir)
	require.NoError(t, err)
	require.NoError(t, kcptestingserver.WaitForReady(t.Context(), server.BaseConfig(t)))
	return server
}
