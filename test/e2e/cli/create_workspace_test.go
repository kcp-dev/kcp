/*
Copyright 2025 The KCP Authors.

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

package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCreateWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	tc := newTestCli(t)
	wsName := "test-create-workspace"

	t.Log("Creating a workspace does not error")
	_, _, err := tc.runPlugin(t, "create-workspace", wsName)
	require.NoError(t, err)
	tc.workspaceShouldExist(t, wsName)

	t.Log("Creating the same workspace again fails")
	_, stderr, err := tc.runPlugin(t, "create-workspace", wsName)
	require.Error(t, err)
	require.Contains(t, stderr.String(), "already exists")

	t.Log("Creating the same workspace with --ignore-existing does not error")
	_, _, err = tc.runPlugin(t, "create-workspace", wsName, "--ignore-existing")
	require.NoError(t, err)
}

func TestCreateWorkspaceCreateContext(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	tc := newTestCli(t)
	wsName := "test-create-workspace-create-context"

	t.Log("Creating a workspace does not error")
	_, _, err := tc.runPlugin(t, "create-workspace", wsName, "--create-context=new-context")
	require.NoError(t, err)
	tc.workspaceShouldExist(t, wsName)

	t.Log("The new context exists in the kubeconfig")
	parsed := tc.readKubeconfig(t)
	kubeCtx, exists := parsed.Contexts["new-context"]
	require.True(t, exists, "expected context new-context to exist in kubeconfig")
	kubeCluster, exists := parsed.Clusters[kubeCtx.Cluster]
	require.True(t, exists, "expected cluster %q to exist in kubeconfig", kubeCtx.Cluster)
	assert.True(t, strings.HasSuffix(kubeCluster.Server, wsName), "expected cluster server to point to the new workspace")
}

func TestCreateWorkspaceEnter(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "cli")

	tc := newTestCli(t)
	wsName := "test-create-workspace-enter"

	t.Log("Creating a workspace and entering it does not error")
	_, _, err := tc.runPlugin(t, "create-workspace", wsName, "--enter")
	require.NoError(t, err)
	tc.workspaceShouldExist(t, wsName)

	t.Log("The current workspace is set to the new workspace")
	stdout, _, err := tc.runPlugin(t, "ws", ".", "--short")
	require.NoError(t, err)
	require.Equal(t, "root:"+wsName+"\n", stdout.String())
}
