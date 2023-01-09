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

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// KcpCliPluginCommand returns the cli args to run the workspace plugin directly or
// via go run (depending on whether NO_GORUN is set).
func KcpCliPluginCommand() []string {
	if NoGoRunEnvSet() {
		return []string{"kubectl", "kcp"}
	} else {
		cmdPath := filepath.Join(RepositoryDir(), "cmd", "kubectl-kcp")
		return []string{"go", "run", cmdPath}
	}
}

// RunKcpCliPlugin runs the kcp workspace plugin with the provided subcommand and
// returns the combined stderr and stdout output.
func RunKcpCliPlugin(t *testing.T, kubeconfigPath string, subcommand []string) []byte {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cmdParts := append(KcpCliPluginCommand(), subcommand...)
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)

	cmd.Env = os.Environ()
	// TODO(marun) Consider configuring the workspace plugin with args instead of this env
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	t.Logf("running: KUBECONFIG=%s %s", kubeconfigPath, strings.Join(cmdParts, " "))

	var output, _, combined bytes.Buffer
	var lock sync.Mutex
	cmd.Stdout = split{a: locked{mu: &lock, w: &combined}, b: &output}
	cmd.Stderr = locked{mu: &lock, w: &combined}
	err := cmd.Run()
	if err != nil {
		t.Logf("kcp plugin output:\n%s", combined.String())
	}
	require.NoError(t, err, "error running kcp plugin command")
	return output.Bytes()
}

// KubectlApply runs kubectl apply -f with the supplied input piped to stdin and returns
// the combined stderr and stdout output.
func KubectlApply(t *testing.T, kubeconfigPath string, input []byte) []byte {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cmdParts := []string{"kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", "-"}
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	_, err = stdin.Write(input)
	require.NoError(t, err)
	// Close to ensure kubectl doesn't keep waiting for input
	err = stdin.Close()
	require.NoError(t, err)

	t.Logf("running: %s", strings.Join(cmdParts, " "))

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("kubectl apply output:\n%s", output)
	}
	require.NoError(t, err)

	return output
}

// Kubectl runs kubectl with the given arguments and returns the combined stderr and stdout.
func Kubectl(t *testing.T, kubeconfigPath string, args ...string) []byte {
	t.Helper()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cmdParts := append([]string{"kubectl", "--kubeconfig", kubeconfigPath}, args...)
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	t.Logf("running: %s", strings.Join(cmdParts, " "))

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("kubectl output:\n%s", output)
	}
	require.NoError(t, err)

	return output
}

type locked struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w locked) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

type split struct {
	a, b io.Writer
}

func (w split) Write(p []byte) (int, error) {
	w.a.Write(p) //nolint:errcheck
	return w.b.Write(p)
}
