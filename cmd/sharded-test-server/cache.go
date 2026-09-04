/*
Copyright 2022 The kcp Authors.

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

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abiosoft/lineprefix"
	"github.com/fatih/color"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"

	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
)

func cacheServerPort(n int) int     { return 8012 + n }
func cacheEtcdClientPort(n int) int { return 8100 + n*2 }
func cacheEtcdPeerPort(n int) int   { return 8101 + n*2 }

// startCacheServer starts cache server instance n. peerURL, when non-empty, enables the
// embedded cache-syncer and points it at cache-0 as the initial peer.
func startCacheServer(ctx context.Context, logDirPath, workingDir, hostIP string, syntheticDelay time.Duration, clientCA *crypto.CA, servingCA *crypto.CA, clientCAPath, externalEtcdServers string, n int, peerURL string) (<-chan error, string, error) {
	prefix := fmt.Sprintf("cache-%d", n)
	cyan := color.New(color.BgHiCyan, color.FgHiWhite).SprintFunc()
	inverse := color.New(color.BgHiWhite, color.FgHiCyan).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(cyan(strings.ToUpper(prefix))),
		lineprefix.Color(color.New(color.FgHiCyan)),
	)
	loggerOut := lineprefix.New(
		lineprefix.Prefix(inverse(strings.ToUpper(prefix))),
		lineprefix.Color(color.New(color.FgHiWhite)),
	)
	cacheWorkingDir := filepath.Join(workingDir, fmt.Sprintf(".kcp-cache-%d", n))
	cachePort := cacheServerPort(n)

	if err := os.MkdirAll(cacheWorkingDir, 0755); err != nil {
		return nil, "", err
	}

	// Generate a serving cert signed by the shared servingCA so peer TLS can use a
	// single CA file rather than per-instance self-signed certs.
	hostnames := sets.New("localhost", hostIP)
	servingCert, err := servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create cache server serving cert: %w", err)
	}
	cacheCertFile := filepath.Join(cacheWorkingDir, "apiserver.crt")
	cacheKeyFile := filepath.Join(cacheWorkingDir, "apiserver.key")
	if err := servingCert.WriteCertConfigFile(cacheCertFile, cacheKeyFile); err != nil {
		return nil, "", fmt.Errorf("failed to write cache server serving cert: %w", err)
	}

	// Read the serving CA once; used both for the kubeconfig and for peer TLS.
	servingCAData, err := os.ReadFile(filepath.Join(workingDir, ".kcp", "serving-ca.crt"))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read serving CA: %w", err)
	}

	// Generate a client certificate for accessing the cache server.
	cacheClientCert := filepath.Join(cacheWorkingDir, "cache-client.crt")
	cacheClientKey := filepath.Join(cacheWorkingDir, "cache-client.key")
	_, err = clientCA.MakeClientCertificate(cacheClientCert, cacheClientKey,
		&user.DefaultInfo{Name: fmt.Sprintf("cache-client-%d", n), Groups: []string{"system:masters"}}, 365)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create cache client cert: %w", err)
	}

	// Use absolute paths so the kubeconfig resolves correctly regardless of CWD.
	absCacheClientCert, err := filepath.Abs(cacheClientCert)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve absolute path for cache client cert: %w", err)
	}
	absCacheClientKey, err := filepath.Abs(cacheClientKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve absolute path for cache client key: %w", err)
	}

	workdir, commandLine := kcptestingserver.Command("cache-server", prefix)
	commandLine = append(
		commandLine,
		fmt.Sprintf("--root-directory=%s", cacheWorkingDir),
		"--bind-address="+hostIP,
		fmt.Sprintf("--secure-port=%d", cachePort),
		fmt.Sprintf("--synthetic-delay=%s", syntheticDelay.String()),
		fmt.Sprintf("--client-ca-file=%s", clientCAPath),
		fmt.Sprintf("--cache-name=%s", prefix),
		fmt.Sprintf("--tls-cert-file=%s", cacheCertFile),
		fmt.Sprintf("--tls-private-key-file=%s", cacheKeyFile),
	)
	if externalEtcdServers != "" {
		commandLine = append(commandLine,
			fmt.Sprintf("--etcd-servers=%s", externalEtcdServers),
		)
	} else {
		commandLine = append(commandLine,
			fmt.Sprintf("--embedded-etcd-client-port=%d", cacheEtcdClientPort(n)),
			fmt.Sprintf("--embedded-etcd-peer-port=%d", cacheEtcdPeerPort(n)),
		)
	}
	commandLine = append(commandLine, fmt.Sprintf("--etcd-prefix=/cache-%d", n))
	if peerURL != "" {
		commandLine = append(commandLine,
			"--run-cache-syncer",
			fmt.Sprintf("--cache-syncer-initial-peer-urls=%s", peerURL),
			fmt.Sprintf("--cache-syncer-peer-ca-file=%s", filepath.Join(workingDir, ".kcp", "serving-ca.crt")),
			fmt.Sprintf("--cache-syncer-peer-cert-file=%s", absCacheClientCert),
			fmt.Sprintf("--cache-syncer-peer-key-file=%s", absCacheClientKey),
		)
	}
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))
	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...) //nolint:gosec
	cmd.Dir = workdir

	logFilePath := filepath.Join(cacheWorkingDir, "kcp.log")
	if logDirPath != "" {
		logFilePath = filepath.Join(logDirPath, fmt.Sprintf("kcp-cache-%d.log", n))
	}
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return nil, "", err
	}
	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, "", err
	}

	writer := helpers.NewHeadWriter(logFile, out)
	cmd.Stdout = writer
	cmd.Stdin = os.Stdin
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return nil, "", err
	}

	terminatedCh := make(chan error, 1)
	go func() {
		terminatedCh <- cmd.Wait()
	}()

	// Build the kubeconfig upfront — we have all data before the server starts.
	cacheKubeconfigPath := filepath.Join(cacheWorkingDir, "cache.kubeconfig")
	cacheServerKubeConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cache": {
				Server:                   fmt.Sprintf("https://localhost:%d", cachePort),
				CertificateAuthorityData: servingCAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"cache": {
				ClientCertificate: absCacheClientCert,
				ClientKey:         absCacheClientKey,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"cache": {
				Cluster:  "cache",
				AuthInfo: "cache",
			},
		},
		CurrentContext: "cache",
	}
	if err := clientcmd.WriteToFile(cacheServerKubeConfig, cacheKubeconfigPath); err != nil {
		return nil, "", err
	}
	loadedKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
	if err != nil {
		return nil, "", err
	}
	cacheClientRestConfig, err := clientcmd.NewNonInteractiveClientConfig(*loadedKubeConfig, "cache", nil, nil).ClientConfig()
	if err != nil {
		return nil, "", err
	}
	cacheClient, err := kcpclientset.NewForConfig(cacheClientRestConfig)
	if err != nil {
		return nil, "", err
	}

	// Wait for readiness.
	logger := klog.FromContext(ctx)
	logger.Info("waiting for the cache server to be up", "n", n)
	for {
		time.Sleep(time.Second)

		select {
		case <-ctx.Done():
			return nil, "", fmt.Errorf("context canceled")
		case err := <-terminatedCh:
			var exitErr *exec.ExitError
			if err == nil {
				return nil, "", fmt.Errorf("cache server %d terminated unexpectedly with exit code 0", n)
			} else if errors.As(err, &exitErr) {
				return nil, "", fmt.Errorf("cache server %d terminated with exit code %d", n, exitErr.ExitCode())
			}
			return nil, "", fmt.Errorf("cache server %d terminated with unknown error: %w", n, err)
		default:
		}

		res := cacheClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			logger.V(3).Info("the cache server is not ready", "n", n, "err", err)
			continue
		}
		var rc int
		res.StatusCode(&rc)
		if rc == http.StatusOK {
			logger.V(3).Info("the cache server is ready", "n", n)
			break
		}
		if bs, err := res.Raw(); err != nil {
			logger.V(3).Info("the cache server is not ready", "n", n, "err", err)
		} else {
			logger.V(3).WithValues("n", n, "rc", rc, "raw", string(bs)).Info("the cache server is not ready")
		}
	}
	fmt.Fprintf(loggerOut, "the cache server is ready\n")
	return terminatedCh, cacheKubeconfigPath, nil
}
