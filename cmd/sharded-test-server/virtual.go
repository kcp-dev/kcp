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
	"embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abiosoft/lineprefix"
	"github.com/fatih/color"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

type headWriter interface {
	io.Writer
	StopOut()
}

type VirtualWorkspace struct {
	index       int
	workDirPath string
	logDirPath  string
	args        []string

	terminatedCh <-chan error
	writer       headWriter
}

func newVirtualWorkspace(ctx context.Context, index int, servingCA *crypto.CA, hostIP string, logDirPath, workDirPath string, clientCA *crypto.CA, cacheServerConfigPath string) (*VirtualWorkspace, error) {
	logger := klog.FromContext(ctx)

	// create serving cert
	hostnames := sets.New[string]("localhost", hostIP)
	logger.Info("Creating vw server serving cert", "index", index, "hostnames", sets.List[string](hostnames))
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
		return nil, fmt.Errorf("failed to create vw client cert: %w", err)
	}

	servingCAPath, err := filepath.Abs(filepath.Join(workDirPath, ".kcp/serving-ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("error getting absolute path for %q: %w", filepath.Join(workDirPath, ".kcp/serving-ca.crt"), err)
	}
	vwClientCertPath, err := filepath.Abs(vwClientCert)
	if err != nil {
		return nil, fmt.Errorf("error getting absolute path for %q: %w", vwClientCert, err)
	}
	vwClientCertKeyPath, err := filepath.Abs(vwClientCertKey)
	if err != nil {
		return nil, fmt.Errorf("error getting absolute path for %q: %w", vwClientCertKey, err)
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

	// write audit policy
	bs, err := embeddedResources.ReadFile("audit-policy.yaml")
	if err != nil {
		return nil, err
	}
	auditPolicyFile := filepath.Join(workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d", index), "audit-policy.yaml")
	if err := os.WriteFile(auditPolicyFile, bs, 0644); err != nil {
		return nil, err
	}

	var args []string
	args = append(args,
		fmt.Sprintf("--kubeconfig=%s", kubeconfigPath),
		fmt.Sprintf("--shard-external-url=https://%s:%d", hostIP, 6443),
		fmt.Sprintf("--cache-kubeconfig=%s", cacheServerConfigPath),
		fmt.Sprintf("--authentication-kubeconfig=%s", authenticationKubeconfigPath),
		fmt.Sprintf("--client-ca-file=%s", clientCAFilePath),
		fmt.Sprintf("--tls-private-key-file=%s", servingKeyFile),
		fmt.Sprintf("--tls-cert-file=%s", servingCertFile),
		fmt.Sprintf("--secure-port=%s", virtualWorkspacePort(index)),
		"--audit-log-maxsize=1024",
		"--audit-log-mode=batch",
		"--audit-log-batch-max-wait=1s",
		"--audit-log-batch-max-size=1000",
		"--audit-log-batch-buffer-size=10000",
		"--audit-log-batch-throttle-burst=15",
		"--audit-log-batch-throttle-enable=true",
		"--audit-log-batch-throttle-qps=10",
		fmt.Sprintf("--audit-policy-file=%s", auditPolicyFile),
	)

	return &VirtualWorkspace{
		index:       index,
		workDirPath: workDirPath,
		logDirPath:  logDirPath,
		args:        args,
	}, nil
}

func (v *VirtualWorkspace) start(ctx context.Context) error {
	prefix := fmt.Sprintf("VW-%d", v.index)
	yellow := color.New(color.BgYellow, color.FgHiWhite).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(yellow(prefix)),
		lineprefix.Color(color.New(color.FgHiYellow)),
	)

	logFilePath := filepath.Join(v.workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/virtualworkspace.log", v.index))
	auditFilePath := filepath.Join(v.workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d", v.index), "audit.log")
	if v.logDirPath != "" {
		logFilePath = filepath.Join(v.logDirPath, fmt.Sprintf("kcp-virtual-workspaces-%d.log", v.index))
		auditFilePath = filepath.Join(v.logDirPath, fmt.Sprintf("kcp-virtual-workspaces-%d-audit.log", v.index))
	}

	commandLine := framework.DirectOrGoRunCommand("virtual-workspaces")
	commandLine = append(commandLine, v.args...)
	commandLine = append(
		commandLine,
		"--authentication-skip-lookup",
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		fmt.Sprintf("--requestheader-client-ca-file=%s", filepath.Join(v.workDirPath, ".kcp/requestheader-ca.crt")),
		"--v=4",
		"--audit-log-path", auditFilePath,
	)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...) //nolint:gosec

	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logFilePath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	v.writer = helpers.NewHeadWriter(logFile, out)
	cmd.Stdout = v.writer
	cmd.Stdin = os.Stdin
	cmd.Stderr = v.writer

	if err := cmd.Start(); err != nil {
		return err
	}

	terminatedCh := make(chan error, 1)
	v.terminatedCh = terminatedCh
	go func() {
		terminatedCh <- cmd.Wait()
	}()

	return nil
}

func (v *VirtualWorkspace) waitForReady(ctx context.Context) (<-chan error, error) {
	// wait for readiness
	logger := klog.FromContext(ctx)
	logger.WithValues("virtual-workspaces", v.index).Info("Waiting for virtual-workspaces /readyz to succeed")

	vwHost := fmt.Sprintf("https://%s", net.JoinHostPort("localhost", virtualWorkspacePort(v.index)))
	kubeconfigPath := filepath.Join(v.workDirPath, fmt.Sprintf(".kcp-virtual-workspaces-%d/virtualworkspace.kubeconfig", v.index))

	if err := wait.PollUntilContextCancel(ctx, time.Millisecond*500, true, func(ctx context.Context) (bool, error) {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("context canceled")
		case rc := <-v.terminatedCh:
			return false, fmt.Errorf("virtual-workspaces terminated with exit code %w", rc)
		default:
		}

		vwConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
			// We override the Server here because virtualworkspace.kubeconfig is
			// for VW->shard but we want to poll the VW endpoint readyz
			&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: vwHost}}).ClientConfig()
		if err != nil {
			return false, fmt.Errorf("failed to create vw config")
		}

		vwClient, err := kcpclientset.NewForConfig(vwConfig)
		if err != nil {
			return false, fmt.Errorf("failed to create vw client")
		}

		res := vwClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			logger.V(3).Info("virtual-workspaces not ready", "err", err)
		} else {
			var rc int
			res.StatusCode(&rc)
			if rc == http.StatusOK {
				return true, nil
			}
			if bs, err := res.Raw(); err != nil {
				logger.V(3).Info("virtual-workspaces not ready", "err", err)
			} else {
				logger.V(3).WithValues("rc", rc, "raw", string(bs)).Info("virtual-workspaces not ready: http")
			}
		}
		return false, nil
	}); err != nil {
		return v.terminatedCh, fmt.Errorf("failed to wait for virtual-workspaces to be ready: %w", err)
	}

	logger.WithValues("virtual-workspaces", v.index).Info("virtual-workspaces ready")

	if !logger.V(3).Enabled() {
		v.writer.StopOut()
	}

	return v.terminatedCh, nil
}
