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

package framework

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/stretchr/testify/require"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
)

// KcpCliPluginCommand returns the expected workdir and cli args to run
// a plugin via go run.
func KcpCliPluginCommand(plugin string) (string, []string, error) {
	repo, err := kcptestinghelpers.RepositoryDir()
	if err != nil {
		return "", nil, err
	}
	workdir := filepath.Join(repo, "staging", "src", "github.com", "kcp-dev", "cli")
	// go run requires `./`, but filepath.Join just omits it
	cmdPath := "./" + filepath.Join("cmd", "kubectl-"+plugin)
	return workdir, []string{"go", "run", cmdPath}, nil
}

// RunKcpCliPlugin runs the kcp plugin with the provided args and
// returns stdout and stderr as bytes.Buffer and an error if any.
// The exitcode can be retreived from the error if it is of type
// *exec.ExitError.
func RunKcpCliPlugin(t kcptesting.TestingT, kubeconfigPath string, plugin string, args []string) (*bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()

	workdir, cmdParts, err := KcpCliPluginCommand(plugin)
	require.NoError(t, err, "error building cli plugin command")

	cmd := exec.CommandContext(t.Context(), cmdParts[0], append(cmdParts[1:], args...)...)
	cmd.Dir = workdir

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	t.Logf("running in %q: KUBECONFIG=%s %s", workdir, kubeconfigPath, strings.Join(cmdParts, " "))

	stdout := &bytes.Buffer{}
	cmd.Stdout = stdout
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	err = cmd.Run()
	if err != nil {
		t.Logf("kcp plugin output:\n  stdout: %s\n  stderr: %s\n", stdout.String(), stderr.String())
	}
	return stdout, stderr, err
}
