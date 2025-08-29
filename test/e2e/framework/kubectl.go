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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
)

func baseCmd(t *testing.T, name string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Env = os.Environ()

	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	return cmd, stdout, stderr
}

// KcpCliPluginCommand returns the expected workdir and cli args to run
// a plugin via go run.
func KcpCliPluginCommand(plugin string) (string, []string) {
	workdir := filepath.Join(kcptestinghelpers.RepositoryDir(), "cli")
	// go run requires `./`, but filepath.Join just omits it
	cmdPath := "./" + filepath.Join("cmd", "kubectl-"+plugin)
	return workdir, []string{"go", "run", cmdPath}
}

// RunKcpCliPlugin runs the kcp plugin with the provided args and
// returns stdout and stderr as bytes.Buffer and an error if any.
// The exitcode can be retreived from the error if it is of type
// *exec.ExitError.
func RunKcpCliPlugin(t *testing.T, kubeconfigPath string, plugin string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	workdir, cmdParts := KcpCliPluginCommand(plugin)
	cmdParts = append(cmdParts, args...)

	cmd, stdout, stderr := baseCmd(t, cmdParts[0], cmdParts[1:]...)
	cmd.Dir = workdir
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	t.Logf("running in %q: KUBECONFIG=%s %s", workdir, kubeconfigPath, strings.Join(cmdParts, " "))

	err := cmd.Run()
	if err != nil {
		t.Logf("kcp plugin output:\n  stdout: %s\n  stderr: %s\n", stdout.String(), stderr.String())
	}
	return stdout, stderr, err
}

func RunKubectl(t *testing.T, kubeconfigPath string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	cmd, stdout, stderr := baseCmd(t, "kubectl", args...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	t.Logf("running: KUBECONFIG=%s kubectl %s", kubeconfigPath, strings.Join(args, " "))

	err := cmd.Run()
	if err != nil {
		t.Logf("kubectl output:\n  stdout: %s\n  stderr: %s\n", stdout.String(), stderr.String())
	}
	return stdout, stderr, err
}
