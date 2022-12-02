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

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
)

func startVirtual(ctx context.Context, index int, servingCA *crypto.CA, hostIP string, logDirPath, workDirPath string, clientCA *crypto.CA) (<-chan error, error) {
	logger := klog.FromContext(ctx)

	prefix := fmt.Sprintf("VW-%d", index)
	yellow := color.New(color.BgYellow, color.FgHiWhite).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(yellow(prefix)),
		lineprefix.Color(color.New(color.FgHiYellow)),
	)

	// create serving cert
	hostnames := sets.NewString("localhost", hostIP)
	logger.Info("Creating vw server serving cert", "index", index, "hostnames", hostnames.List())
	cert, err := servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}
	servingKeyFile := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/apiserver.key", index))
	servingCertFile := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/apiserver.crt", index))
	if err := cert.WriteCertConfigFile(servingCertFile, servingKeyFile); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	// create client cert used to talk to kcp
	vwClientCert := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/shard-client-cert.crt", index))
	vwClientCertKey := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/shard-client-cert.key", index))
	shardUser := &user.DefaultInfo{Name: fmt.Sprintf("kcp-vw-%d", index), Groups: []string{"system:masters"}}
	_, err = clientCA.MakeClientCertificate(vwClientCert, vwClientCertKey, shardUser, 365)
	if err != nil {
		fmt.Printf("failed to create vw client cert: %v\n", err)
		os.Exit(1)
	}

	servingCAPath, err := filepath.Abs(filepath.Join(workDirPath, ".kcp/serving-ca.crt"))
	if err != nil {
		fmt.Printf("error getting absolute path for %q: %v\n", filepath.Join(workDirPath, ".kcp/serving-ca.crt"), err)
		os.Exit(1)
	}
	vwClientCertPath, err := filepath.Abs(vwClientCert)
	if err != nil {
		fmt.Printf("error getting absolute path for %q: %v\n", vwClientCert, err)
		os.Exit(1)
	}
	vwClientCertKeyPath, err := filepath.Abs(vwClientCertKey)
	if err != nil {
		fmt.Printf("error getting absolute path for %q: %v\n", vwClientCertKey, err)
		os.Exit(1)
	}

	virtualWorkspaceKubeConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"shard": {
				Server:               fmt.Sprintf("https://localhost:%d/clusters/system:admin", 6444+index),
				CertificateAuthority: servingCAPath,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"shard": {
				Cluster:  "shard",
				AuthInfo: "virtualworkspace",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"virtualworkspace": {
				ClientCertificate: vwClientCertPath,
				ClientKey:         vwClientCertKeyPath,
			},
		},
		CurrentContext: "shard",
	}
	kubeconfigPath := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/virtualworkspace.kubeconfig", index))
	err = clientcmd.WriteToFile(virtualWorkspaceKubeConfig, kubeconfigPath)
	if err != nil {
		fmt.Printf("failed to write vw kubeconfig: %v", err)
		os.Exit(1)
	}

	authenticationKubeconfigPath := filepath.Join(workDirPath, fmt.Sprintf(".kcp-%d", index), "admin.kubeconfig")
	clientCAFilePath := filepath.Join(workDirPath, ".kcp", "client-ca.crt")

	commandLine := framework.DirectOrGoRunCommand("virtual-workspaces")
	commandLine = append(
		commandLine,
		fmt.Sprintf("--kubeconfig=%s", kubeconfigPath),
		fmt.Sprintf("--authentication-kubeconfig=%s", authenticationKubeconfigPath),
		"--authentication-skip-lookup",
		fmt.Sprintf("--client-ca-file=%s", clientCAFilePath),
		fmt.Sprintf("--tls-private-key-file=%s", servingKeyFile),
		fmt.Sprintf("--tls-cert-file=%s", servingCertFile),
		fmt.Sprintf("--secure-port=%d", 7444+index),
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		fmt.Sprintf("--requestheader-client-ca-file=%s", filepath.Join(workDirPath, ".kcp/requestheader-ca.crt")),
		"--v=4",
	)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...) //nolint:gosec

	logFilePath := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/virtualworkspace.log", index))
	if logDirPath != "" {
		logFilePath = filepath.Join(logDirPath, fmt.Sprintf("kcp-virtual-workspaces-%d.log", index))
	}

	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	writer := helpers.NewHeadWriter(logFile, out)
	cmd.Stdout = writer
	cmd.Stdin = os.Stdin
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	terminatedCh := make(chan error, 1)
	go func() {
		terminatedCh <- cmd.Wait()
	}()

	// wait for readiness
	logger.WithValues("virtual-workspaces", index).Info("Waiting for virtual-workspaces /readyz to succeed")
	for {
		time.Sleep(100 * time.Millisecond)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled")
		case rc := <-terminatedCh:
			return nil, fmt.Errorf("virtual-workspaces terminated with exit code %d", rc)
		default:
		}

		vwHost := fmt.Sprintf("https://localhost:%d", 7444+index)
		vwConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
		// We override the Server here because virtualworkspace.kubeconfig is
		// for VW->shard but we want to poll the VW endpoint readyz
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: vwHost}}).ClientConfig()
		if err != nil {
			logger.Error(err, "failed to create vw config")
			continue
		}

		vwClient, err := kcpclientset.NewForConfig(vwConfig)
		if err != nil {
			logger.Error(err, "failed to create vw client")
			continue
		}

		res := vwClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			logger.V(3).Info("virtual-workspaces not ready", "err", err)
		} else {
			var rc int
			res.StatusCode(&rc)
			if rc == http.StatusOK {
				break
			}
			if bs, err := res.Raw(); err != nil {
				logger.V(3).Info("virtual-workspaces not ready", "err", err)
			} else {
				logger.V(3).WithValues("rc", rc, "raw", string(bs)).Info("virtual-workspaces not ready: http")
			}
		}
	}

	logger.WithValues("virtual-workspaces", index).Info("virtual-workspaces ready")

	if !logger.V(3).Enabled() {
		writer.StopOut()
	}

	return terminatedCh, nil
}
