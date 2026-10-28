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

package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// eventuallyInterval is the pause between attempts of `eventually`.
const eventuallyInterval = 100 * time.Millisecond

// eventually re-runs a command until it exits 0 or wait.ForeverTestTimeout passes.
// stdout and stderr of the last attempt are available to stdout/stderr/cmp.
func eventually(ctx context.Context) func(*testscript.TestScript, bool, []string) {
	return func(ts *testscript.TestScript, neg bool, args []string) {
		if neg || len(args) < 1 {
			ts.Fatalf("usage: eventually program [args...]")
		}

		var execErr error
		err := wait.PollUntilContextTimeout(ctx, eventuallyInterval, wait.ForeverTestTimeout, true, func(context.Context) (bool, error) {
			execErr = ts.Exec(args[0], args[1:]...)
			return execErr == nil, nil
		})
		if err != nil {
			ts.Fatalf("%s did not succeed within %s: %v", args[0], wait.ForeverTestTimeout, execErr)
		}
	}
}

// TestScript runs the txtar scripts in testdata/script.
//
// txtar scripts are run as subtests and can be addressed with the
// filename, omitting the `.txtar` suffix:
//
//	-run TestScript/create_workspace
func TestScript(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	// validate the plugins are actually built
	// TODO(ntnn): If the e2e tests are reused as compliance tests this
	// needs to be removed/moved into a different harness.
	repo, err := kcptestinghelpers.RepositoryDir()
	require.NoError(t, err)
	binDir := filepath.Join(repo, "bin")
	for _, plugin := range []string{"kubectl-kcp", "kubectl-ws", "kubectl-create-workspace"} {
		if _, err := os.Stat(filepath.Join(binDir, plugin)); err != nil {
			t.Skipf("%s not built: run make build-e2e", plugin)
		}
	}

	server := kcptesting.SharedKcpServer(t)

	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		// allows updating files the cli output is compared against by setting UPDATE=true
		UpdateScripts: os.Getenv("UPDATE") != "",
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"eventually": eventually(t.Context()),
		},
		Setup: func(env *testscript.Env) error {
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))

			orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
			env.Setenv("ORG", orgPath.String())

			kubeconfig := framework.WriteWorkspaceKubeconfig(t, server, orgPath, env.WorkDir)
			env.Setenv("KUBECONFIG", kubeconfig)

			return nil
		},
	})
}
