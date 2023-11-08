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

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func startCacheServer(ctx context.Context, logDirPath, workingDir string, syntheticDelay time.Duration) (<-chan error, string, error) {
	cyan := color.New(color.BgHiCyan, color.FgHiWhite).SprintFunc()
	inverse := color.New(color.BgHiWhite, color.FgHiCyan).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(cyan(strings.ToUpper("cache"))),
		lineprefix.Color(color.New(color.FgHiCyan)),
	)
	loggerOut := lineprefix.New(
		lineprefix.Prefix(inverse(strings.ToUpper("cache"))),
		lineprefix.Color(color.New(color.FgHiWhite)),
	)
	cacheWorkingDir := filepath.Join(workingDir, ".kcp-cache")
	cachePort := 8012
	commandLine := framework.DirectOrGoRunCommand("cache-server")
	commandLine = append(
		commandLine,
		fmt.Sprintf("--root-directory=%s", cacheWorkingDir),
		"--embedded-etcd-client-port=8010",
		"--embedded-etcd-peer-port=8011",
		fmt.Sprintf("--secure-port=%d", cachePort),
		fmt.Sprintf("--synthetic-delay=%s", syntheticDelay.String()),
	)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))
	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...) //nolint:gosec

	logFilePath := filepath.Join(logDirPath, ".kcp-cache", "out.log")
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

	// wait for readiness
	logger := klog.FromContext(ctx)
	logger.Info("waiting for the cache server to be up")
	cacheKubeconfigPath := filepath.Join(cacheWorkingDir, "cache.kubeconfig")
	for {
		time.Sleep(time.Second)

		select {
		case <-ctx.Done():
			return nil, "", fmt.Errorf("context canceled")
		case err := <-terminatedCh:
			var exitErr *exec.ExitError
			if err == nil {
				return nil, "", fmt.Errorf("the cache server terminated unexpectedly with exit code 0")
			} else if errors.As(err, &exitErr) {
				return nil, "", fmt.Errorf("the cache server terminated with exit code %d", exitErr.ExitCode())
			}
			return nil, "", fmt.Errorf("the cache server terminated with unknown error: %w", err)
		default:
		}

		if _, err := os.Stat(filepath.Join(cacheWorkingDir, "apiserver.crt")); os.IsNotExist(err) {
			logger.V(3).Info("failed to read the cache server certificate file", "err", err, "path", filepath.Join(cacheWorkingDir, "apiserver.crt"))
			continue
		}

		if _, err := os.Stat(cacheKubeconfigPath); os.IsNotExist(err) {
			cacheServerCert, err := os.ReadFile(filepath.Join(cacheWorkingDir, "apiserver.crt"))
			if err != nil {
				return nil, "", err
			}
			cacheServerKubeConfig := clientcmdapi.Config{
				Clusters: map[string]*clientcmdapi.Cluster{
					"cache": {
						Server:                   fmt.Sprintf("https://localhost:%d", cachePort),
						CertificateAuthorityData: cacheServerCert,
					},
				},
				Contexts: map[string]*clientcmdapi.Context{
					"cache": {
						Cluster: "cache",
					},
				},
				CurrentContext: "cache",
			}
			if err = clientcmd.WriteToFile(cacheServerKubeConfig, cacheKubeconfigPath); err != nil {
				return nil, "", err
			}
		}

		cacheServerKubeConfig, err := clientcmd.LoadFromFile(cacheKubeconfigPath)
		if err != nil {
			return nil, "", err
		}
		cacheClientConfig := clientcmd.NewNonInteractiveClientConfig(*cacheServerKubeConfig, "cache", nil, nil)
		cacheClientRestConfig, err := cacheClientConfig.ClientConfig()
		if err != nil {
			return nil, "", err
		}
		cacheClient, err := kcpclientset.NewForConfig(cacheClientRestConfig)
		if err != nil {
			return nil, "", err
		}

		res := cacheClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			logger.V(3).Info("the cache server is not ready", "err", err)
		} else {
			var rc int
			res.StatusCode(&rc)
			if rc == http.StatusOK {
				logger.V(3).Info("the cache server is ready")
				break
			}
			if bs, err := res.Raw(); err != nil {
				logger.V(3).Info("the cache server is not ready", "err", err)
			} else {
				logger.V(3).WithValues("rc", rc, "raw", string(bs)).Info("the cache server is not ready")
			}
		}
	}
	fmt.Fprintf(loggerOut, "the cache server is ready\n")
	return terminatedCh, cacheKubeconfigPath, nil
}
