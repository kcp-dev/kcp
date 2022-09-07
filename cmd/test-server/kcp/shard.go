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

package shard

import (
	"context"
	"embed"
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
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/test-server/helpers"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

// Start starts a kcp shard server. It returns with nil when it is ready, or
// when the context is done.
func Start(ctx context.Context, name, runtimeDir, logFilePath string, args []string) (<-chan error, error) {
	// setup color output
	prefix := strings.ToUpper(name)
	blue := color.New(color.BgBlue, color.FgHiWhite).SprintFunc()
	inverse := color.New(color.BgHiWhite, color.FgBlue).SprintFunc()
	out := lineprefix.New(
		lineprefix.Prefix(blue(prefix)),
		lineprefix.Color(color.New(color.FgHiBlue)),
	)
	successOut := lineprefix.New(
		lineprefix.Prefix(inverse(fmt.Sprintf(" %s ", prefix))),
		lineprefix.Color(color.New(color.FgHiWhite)),
	)

	// write audit policy
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		return nil, err
	}
	bs, err := embeddedResources.ReadFile("audit-policy.yaml")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "audit-policy.yaml"), bs, 0644); err != nil {
		return nil, err
	}

	// setup command
	commandLine := append(framework.StartKcpCommand(), framework.TestServerArgs()...)
	commandLine = append(commandLine, args...)
	commandLine = append(commandLine,
		"--audit-log-maxsize", "1024",
		"--audit-log-mode=batch",
		"--audit-log-batch-max-wait=1s",
		"--audit-log-batch-max-size=1000",
		"--audit-log-batch-buffer-size=10000",
		"--audit-log-batch-throttle-burst=15",
		"--audit-log-batch-throttle-enable=true",
		"--audit-log-batch-throttle-qps=10",
		"--audit-policy-file", filepath.Join(runtimeDir, "audit-policy.yaml"),
	)
	fmt.Fprintf(out, "running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...)
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

	go func() {
		<-ctx.Done()
		if err := cmd.Process.Kill(); err != nil {
			klog.ErrorS(err, "failed to kill process")
		}
	}()

	terminatedCh := make(chan error, 1)
	go func() {
		terminatedCh <- cmd.Wait()
	}()

	// wait for admin.kubeconfig
	kubeconfigPath := filepath.Join(runtimeDir, "admin.kubeconfig")
	klog.Infof("Waiting for %s", kubeconfigPath)
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled")
		case err := <-terminatedCh:
			var exitErr *exec.ExitError
			if err == nil {
				return nil, fmt.Errorf("kcp shard %s terminated unexpectedly with exit code 0", name)
			} else if errors.As(err, &exitErr) {
				return nil, fmt.Errorf("kcp shard %s terminated with exit code %d", name, exitErr.ExitCode())
			}
			return nil, fmt.Errorf("kcp shard %s terminated with unknown error: %w", name, err)
		default:
		}
		if _, err := os.Stat(kubeconfigPath); err == nil {
			break
		}
		time.Sleep(time.Millisecond * 1000)
	}
	klog.Infof("Found %s", kubeconfigPath)

	// wait for readiness
	klog.Infof("Waiting for %s shard /readyz to succeed", name)
	for {
		time.Sleep(time.Second)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context canceled")
		case err := <-terminatedCh:
			var exitErr *exec.ExitError
			if err == nil {
				return nil, fmt.Errorf("kcp shard %s terminated unexpectedly with exit code 0", name)
			} else if errors.As(err, &exitErr) {
				return nil, fmt.Errorf("kcp shard %s terminated with exit code %d", name, exitErr.ExitCode())
			}
			return nil, fmt.Errorf("kcp shard %s terminated with unknown error: %w", name, err)
		default:
		}

		// intentionally load again every iteration because it can change
		configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath},
			&clientcmd.ConfigOverrides{CurrentContext: "system:admin"},
		)
		config, err := configLoader.ClientConfig()
		if err != nil {
			continue
		}
		kcpClient, err := kcpclient.NewClusterForConfig(config)
		if err != nil {
			klog.Errorf("Failed to create kcp client: %v", err)
			continue
		}

		res := kcpClient.RESTClient().Get().AbsPath("/readyz").Do(ctx)
		if err := res.Error(); err != nil {
			klog.V(3).Infof("kcp shard %s not ready: %v", name, err)
		} else {
			var rc int
			res.StatusCode(&rc)
			if rc == http.StatusOK {
				break
			}
			if bs, err := res.Raw(); err != nil {
				klog.V(3).Infof("kcp shard %s not ready: %v", name, err)
			} else {
				klog.V(3).Infof("kcp shard %s not ready: http %d: %s", name, rc, string(bs))
			}
		}
	}
	if !klog.V(3).Enabled() {
		writer.StopOut()
	}
	fmt.Fprintf(successOut, "shard is ready\n")

	return terminatedCh, nil
}
