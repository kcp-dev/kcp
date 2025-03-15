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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
)

// NewOrganizationFixture creates a workspace under root.
//
// Deprecated: use NewWorkspaceFixture with WithType(core.RootCluster.Path(),
// "organization") instead. There is no organization concept in kcp.
func NewOrganizationFixture(t *testing.T, server kcptestingserver.RunningServer, options ...kcptesting.UnprivilegedWorkspaceOption) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()
	return kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), append(options, kcptesting.WithType(core.RootCluster.Path(), "organization"))...)
}

// NewRootShardOrganizationFixture creates an organization workspace on the root
// shard.
func NewRootShardOrganizationFixture[O kcptesting.WorkspaceOption](t *testing.T, server kcptestingserver.RunningServer, options ...O) (logicalcluster.Path, *tenancyv1alpha1.Workspace) {
	t.Helper()

	rootConfig := server.RootShardSystemMasterBaseConfig(t)
	rootClusterClient, err := kcpclientset.NewForConfig(rootConfig)
	require.NoError(t, err, "failed to construct client for server")

	cfg := server.BaseConfig(t)
	clusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	ws := kcptesting.NewLowLevelWorkspaceFixture(t, rootClusterClient, clusterClient, core.RootCluster.Path(), append(options, O(kcptesting.WithType(core.RootCluster.Path(), "organization")))...)
	return core.RootCluster.Path().Join(ws.Name), ws
}
