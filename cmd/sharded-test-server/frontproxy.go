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

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func startFrontProxy(
	ctx context.Context,
	args []string,
	servingCA *crypto.CA,
	hostIP string,
	logDirPath, workDirPath string,
	vwPort string,
) error {
	blue := color.New(color.BgGreen, color.FgBlack).SprintFunc()
	inverse := color.New(color.BgHiWhite, color.FgGreen).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(blue(" PROXY ")),
		lineprefix.Color(color.New(color.FgHiGreen)),
	)
	successOut := lineprefix.New(
		lineprefix.Prefix(inverse(" PROXY ")),
		lineprefix.Color(color.New(color.FgHiWhite)),
	)

	logger := klog.FromContext(ctx)

	type mappingEntry struct {
		Path            string `json:"path"`
		Backend         string `json:"backend"`
		BackendServerCA string `json:"backend_server_ca"`
		ProxyClientCert string `json:"proxy_client_cert"`
		ProxyClientKey  string `json:"proxy_client_key"`
	}

	mappings := []mappingEntry{
		{
			Path: "/services/",
			// TODO: support multiple virtual workspace backend servers
			Backend:         fmt.Sprintf("https://localhost:%s", vwPort),
			BackendServerCA: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
			ProxyClientCert: filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.crt"),
			ProxyClientKey:  filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.key"),
		},
		{
			Path: "/clusters/",
			// TODO: support multiple shard backend servers
			Backend:         "https://localhost:6444",
			BackendServerCA: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
			ProxyClientCert: filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.crt"),
			ProxyClientKey:  filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.key"),
		},
	}

	mappingsYAML, err := yaml.Marshal(mappings)
	if err != nil {
		return fmt.Errorf("error marshaling mappings yaml: %w", err)
	}

	if err := os.WriteFile(filepath.Join(workDirPath, ".kcp-front-proxy/mapping.yaml"), mappingsYAML, 0644); err != nil {
		return fmt.Errorf("failed to create front-proxy mapping.yaml: %w", err)
	}

	// write root shard kubeconfig
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(workDirPath, ".kcp-0/admin.kubeconfig")}, nil)
	raw, err := configLoader.RawConfig()
	if err != nil {
		return err
	}
	raw.CurrentContext = "system:admin"
	if err := clientcmdapi.MinifyConfig(&raw); err != nil {
		return err
	}
	if err := clientcmd.WriteToFile(raw, filepath.Join(workDirPath, ".kcp/root.kubeconfig")); err != nil {
		return err
	}

	// create serving cert
	hostnames := sets.NewString("localhost", hostIP)
	logger.Info("creating kcp-front-proxy serving cert with hostnames", "hostnames", hostnames)
	cert, err := servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return fmt.Errorf("failed to create server cert: %w", err)
	}
	if err := cert.WriteCertConfigFile(filepath.Join(workDirPath, ".kcp-front-proxy/apiserver.crt"), filepath.Join(workDirPath, ".kcp-front-proxy/apiserver.key")); err != nil {
		return fmt.Errorf("failed to write server cert: %w", err)
	}

	// run front-proxy command
	commandLine := append(framework.DirectOrGoRunCommand("kcp-front-proxy"),
		fmt.Sprintf("--mapping-file=%s", filepath.Join(workDirPath, ".kcp-front-proxy/mapping.yaml")),
		fmt.Sprintf("--root-directory=%s", filepath.Join(workDirPath, ".kcp-front-proxy")),
		fmt.Sprintf("--root-kubeconfig=%s", filepath.Join(workDirPath, ".kcp/root.kubeconfig")),
		fmt.Sprintf("--shards-kubeconfig=%s", filepath.Join(workDirPath, ".kcp-front-proxy/shards.kubeconfig")),
		fmt.Sprintf("--client-ca-file=%s", filepath.Join(workDirPath, ".kcp/client-ca.crt")),
		fmt.Sprintf("--tls-cert-file=%s", filepath.Join(workDirPath, ".kcp-front-proxy/apiserver.crt")),
		fmt.Sprintf("--tls-private-key-file=%s", filepath.Join(workDirPath, ".kcp-front-proxy/apiserver.key")),
		"--secure-port=6443",
		"--v=4",
	)
	commandLine = append(commandLine, args...)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...)

	logFilePath := filepath.Join(workDirPath, ".kcp-front-proxy/proxy.log")
	if logDirPath != "" {
		logFilePath = filepath.Join(logDirPath, "kcp-front-proxy.log")
	}

	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	writer := helpers.NewHeadWriter(logFile, out)
	cmd.Stdout = writer
	cmd.Stdin = os.Stdin
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		if err := cmd.Process.Kill(); err != nil {
			logger.Error(err, "failed to kill process")
		}
	}()

	terminatedCh := make(chan int, 1)
	go func() {
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				terminatedCh <- exitErr.ExitCode()
			}
		} else {
			terminatedCh <- 0
		}
	}()

	// wait for readiness
	logger.Info("waiting for kcp-front-proxy to be up")
	for {
		time.Sleep(time.Second)

		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled")
		case rc := <-terminatedCh:
			return fmt.Errorf("kcp-front-proxy terminated with exit code %d", rc)
		default:
		}

		// intentionally load again every iteration because it can change
		configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: filepath.Join(workDirPath, ".kcp/admin.kubeconfig")},
			&clientcmd.ConfigOverrides{CurrentContext: "base"},
		)
		config, err := configLoader.ClientConfig()
		if err != nil {
			continue
		}
		kcpClient, err := kcpclientset.NewForConfig(config)
		if err != nil {
			logger.Error(err, "failed to create kcp client")
			continue
		}

		res := kcpClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			logger.V(3).Info("kcp-front-proxy not ready", "err", err)
		} else {
			var rc int
			res.StatusCode(&rc)
			if rc == http.StatusOK {
				break
			}
			if bs, err := res.Raw(); err != nil {
				logger.V(3).Info("kcp-front-proxy not ready", "err", err)
			} else {
				logger.V(3).WithValues("rc", rc, "raw", string(bs)).Info("kcp-front-proxy not ready: http")
			}
		}
	}
	if !logger.V(3).Enabled() {
		writer.StopOut()
	}
	fmt.Fprintf(successOut, "kcp-front-proxy is ready\n")

	return nil
}

func writeAdminKubeConfig(hostIP string, workDirPath string) error {
	baseHost := fmt.Sprintf("https://%s:6443", hostIP)

	var kubeConfig clientcmdapi.Config
	kubeConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"kcp-admin": {
			ClientKey:         filepath.Join(workDirPath, ".kcp/kcp-admin.key"),
			ClientCertificate: filepath.Join(workDirPath, ".kcp/kcp-admin.crt"),
		},
	}
	kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{
		"root": {
			Server:               baseHost + "/clusters/root",
			CertificateAuthority: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
		},
		"base": {
			Server:               baseHost,
			CertificateAuthority: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"root": {Cluster: "root", AuthInfo: "kcp-admin"},
		"base": {Cluster: "base", AuthInfo: "kcp-admin"},
	}
	kubeConfig.CurrentContext = "root"

	if err := clientcmdapi.FlattenConfig(&kubeConfig); err != nil {
		return err
	}

	return clientcmd.WriteToFile(kubeConfig, filepath.Join(workDirPath, ".kcp/admin.kubeconfig"))
}

func writeShardKubeConfig(workDirPath string) error {
	var kubeConfig clientcmdapi.Config
	kubeConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"shard-admin": {
			ClientKey:         filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.key"),
			ClientCertificate: filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.crt"),
		},
	}
	kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{
		"base": {
			CertificateAuthority: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"base": {Cluster: "base", AuthInfo: "shard-admin"},
	}
	kubeConfig.CurrentContext = "base"

	if err := clientcmdapi.FlattenConfig(&kubeConfig); err != nil {
		return err
	}

	return clientcmd.WriteToFile(kubeConfig, filepath.Join(workDirPath, ".kcp-front-proxy/shards.kubeconfig"))
}

func writeLogicalClusterAdminKubeConfig(hostIP, workDirPath string) error {
	baseHost := fmt.Sprintf("https://%s:6443", hostIP)

	var kubeConfig clientcmdapi.Config
	kubeConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"logical-cluster-admin": {
			ClientKey:         filepath.Join(workDirPath, ".kcp/logical-cluster-admin.key"),
			ClientCertificate: filepath.Join(workDirPath, ".kcp/logical-cluster-admin.crt"),
		},
	}
	kubeConfig.Clusters = map[string]*clientcmdapi.Cluster{
		"base": {
			Server:               baseHost,
			CertificateAuthority: filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
		},
	}
	kubeConfig.Contexts = map[string]*clientcmdapi.Context{
		"base": {Cluster: "base", AuthInfo: "logical-cluster-admin"},
	}
	kubeConfig.CurrentContext = "base"

	if err := clientcmdapi.FlattenConfig(&kubeConfig); err != nil {
		return err
	}

	return clientcmd.WriteToFile(kubeConfig, filepath.Join(workDirPath, ".kcp/logical-cluster-admin.kubeconfig"))
}
