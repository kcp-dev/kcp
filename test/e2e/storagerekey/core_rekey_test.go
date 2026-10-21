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

package storagerekey

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// coreRekeyShards is the number of shards the test's own server runs: the
// re-keyed exports live in root, so with two shards the re-keying controller
// and the migrators are exercised across shards and through the front-proxy.
const coreRekeyShards = 2

// coreExports are the APIExports in the root workspace that serve kcp's own
// APIs. Their identities are what the shards, the front-proxy and the
// virtual workspaces resolve at startup and inject into wildcard requests;
// migration.kcp.io also serves the re-keying type itself.
var coreExports = []string{
	"migration.kcp.io",
	"shards.core.kcp.io",
	"topology.kcp.io",
	"tenancy.kcp.io",
	"cache.kcp.io",
}

// coreRekeyTimeout bounds one core export re-keying. A tenancy.kcp.io
// re-keying drains the tenancy binding of every workspace on every shard.
const coreRekeyTimeout = 2 * time.Minute

// alternateKey is the storage key round 1 moves a core export onto. With
// identity decoupling a fresh installation already keys core exports by
// their well-known identities ("system-<export>"), so the round uses a
// distinct key; moving back onto the identity is round 2.
func alternateKey(export string) string {
	return "rekeyed-" + export
}

// TestCoreAPIExportStorageRekey re-keys the storage of kcp's core APIExports
// in the root workspace, all of them, onto well-known storage keys and back
// onto their identities, and checks after every round that the platform
// still works: the workspaces created before the re-keyings are still there
// with their UIDs, new workspaces can be created on every shard and are
// reachable by path, and every shard serves the tenancy data under the
// unchanged identity. The re-keyings are requested from an ops workspace
// that binds migration.kcp.io, which is itself one of the re-keyed exports.
// Completed re-keyings are deleted between rounds, which is what lifts the
// cooldown between re-keyings of one export.
//
// The identities never change, so no component has to re-resolve anything:
// the shards' and the front-proxy's wildcard requests keep using the same
// identity while the data moves underneath.
//
// The test runs on a sharded server of its own: a tenancy.kcp.io re-keying
// fences every workspace on every shard while it drains, which would disrupt
// any other test sharing the server.
//
//nolint:paralleltest // the test has a server of its own and runs in the serial suite, see framework.SerialSuite.
func TestCoreAPIExportStorageRekey(t *testing.T) {
	framework.Suite(t, framework.SerialSuite)

	server := framework.PrivateShardedKcpServer(t, coreRekeyShards)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := newRekeyClients(t, server)
	shardNames := server.ShardNames()
	require.NotEmpty(t, shardNames, "the test server reports no shards")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	// the ops workspace goes to the last shard, a non-root shard whenever
	// there is one: the re-keyed exports live in root, so the re-keying
	// controller has to reach them across shards.
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithShard(shardNames[len(shardNames)-1]))
	bindMigrationAPI(ctx, t, c, opsPath)

	t.Logf("Create one consumer workspace per shard %v and a Partition; they must survive every re-keying", shardNames)
	before := map[string]types.UID{}
	for _, shard := range shardNames {
		_, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer-%s", shard), kcptesting.WithShard(shard))
		before[ws.Name] = ws.UID
	}
	partition, err := c.kcp.Cluster(orgPath).TopologyV1alpha1().Partitions().Create(ctx, &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{Name: "survivor"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	identities := map[string]string{}
	for _, export := range coreExports {
		got, err := c.kcp.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, export, metav1.GetOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, got.Status.IdentityHash, "root:%s has no identity", export)
		identities[export] = got.Status.IdentityHash
	}

	// round 1 moves every core export onto an alternate key, round 2 moves
	// it back onto its identity (an empty target key).
	rounds := []struct {
		name   string
		target func(export string) string
	}{
		{name: "alternate", target: alternateKey},
		{name: "identity", target: func(string) string { return "" }},
	}
	for round, r := range rounds {
		for _, export := range coreExports {
			name := fmt.Sprintf("%s-%s", export, r.name)
			want := r.target(export)
			if want == "" {
				want = identities[export]
			}
			t.Logf("Round %d: re-key root:%s onto storage key %s", round+1, export, want)

			requestRekey(ctx, t, c, opsPath, migrationv1alpha1.ExportReference{Path: core.RootCluster.Path().String(), Name: export}, name, r.target(export))

			got := waitForCoreRekeyCompleted(ctx, t, c, opsPath, name, shardNames)
			require.Equal(t, want, got, "re-keying %s resolved an unexpected target key", name)
			requireCoreExportIdentity(ctx, t, c, export, identities[export])
			deleteCompletedRekey(ctx, t, c, opsPath, name)
		}

		t.Logf("Round %d: every core export is re-keyed; check the platform still works", round+1)
		requireWorkspacesSurvived(ctx, t, c, orgPath, before)
		got, err := c.kcp.Cluster(orgPath).TopologyV1alpha1().Partitions().Get(ctx, partition.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, partition.UID, got.UID, "the Partition must survive the topology.kcp.io re-keying with its UID")

		for _, shard := range shardNames {
			path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("round-%d-%s", round+1, shard), kcptesting.WithShard(shard))
			_, err := c.kube.Cluster(path).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "the new workspace %s should be reachable by path", path)
		}

		shardConfigs := map[string]*rest.Config{}
		for _, shard := range shardNames {
			shardConfigs[shard] = server.ShardSystemMasterBaseConfig(t, shard)
		}
		requireShardsServeWorkspaces(ctx, t, shardConfigs, identities["tenancy.kcp.io"], before)
	}
}

// waitForCoreRekeyCompleted waits for the re-keying to complete and returns
// the resolved target key. Unlike waitForRekeyCompleted it does not check the
// binding counters: it checks that every shard reported, and leaves the data
// checks to the caller.
func waitForCoreRekeyCompleted(ctx context.Context, t *testing.T, c rekeyClients, opsPath logicalcluster.Path, name string, shardNames []string) string {
	t.Helper()
	var target string
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rekey, err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys().Get(ctx, name, metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEqual(collect, migrationv1alpha1.APIExportStorageRekeyFailed, rekey.Status.Phase, "re-keying failed: %v", rekey.Status.Conditions)
		require.Equal(collect, migrationv1alpha1.APIExportStorageRekeyCompleted, rekey.Status.Phase,
			"re-keying phase %s, migrated %d/%d, shards %s", rekey.Status.Phase, rekey.Status.MigratedBindings, rekey.Status.TotalBindings, describeShardProgress(rekey.Status.Shards))
		require.True(collect, conditions.IsTrue(rekey, migrationv1alpha1.StorageRekeyDrained), "expected Drained=True")
		reported := sets.New[string]()
		for _, shard := range rekey.Status.Shards {
			reported.Insert(shard.Shard)
		}
		require.True(collect, reported.HasAll(shardNames...), "expected drain reports from shards %v, got %v", shardNames, sets.List(reported))
		require.NotEmpty(collect, rekey.Status.TargetKey)
		target = rekey.Status.TargetKey
	}, coreRekeyTimeout, 250*time.Millisecond, "waiting for re-keying %s to complete", name)
	return target
}

// requireCoreExportIdentity asserts the root APIExport still has the given
// identity, carries no active-rekey marker and is valid.
func requireCoreExportIdentity(ctx context.Context, t *testing.T, c rekeyClients, export, identity string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		got, err := c.kcp.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, export, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, identity, got.Status.IdentityHash, "root:%s must keep its identity", export)
		require.NotContains(collect, got.Annotations, migrationv1alpha1.ActiveRekeyAnnotationKey, "root:%s should carry no active-rekey marker", export)
		require.True(collect, conditions.IsTrue(got, apisv1alpha2.APIExportIdentityValid), "root:%s should be IdentityValid: %v", export, got.Status.Conditions)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// deleteCompletedRekey deletes the re-keying, which lifts the cooldown the
// admission plugin keeps between completed re-keyings of the same export.
// Right after a migration.kcp.io re-keying the re-keying API itself may be
// briefly unavailable while its bound CRD is rebuilt, hence the retry.
func deleteCompletedRekey(ctx context.Context, t *testing.T, c rekeyClients, opsPath logicalcluster.Path, name string) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportStorageRekeys().Delete(ctx, name, metav1.DeleteOptions{})
		if err == nil || apierrors.IsNotFound(err) {
			return true, ""
		}
		return false, fmt.Sprintf("deleting re-keying %s: %v", name, err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// requireWorkspacesSurvived asserts the workspaces created before the
// re-keyings are still listed with their UIDs, and Ready.
func requireWorkspacesSurvived(ctx context.Context, t *testing.T, c rekeyClients, orgPath logicalcluster.Path, before map[string]types.UID) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.kcp.Cluster(orgPath).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		require.NoError(collect, err)
		got := map[string]types.UID{}
		for _, ws := range list.Items {
			if uid, ok := before[ws.Name]; ok {
				require.Equal(collect, uid, ws.UID, "workspace %s must survive the re-keyings with its UID", ws.Name)
				require.Equal(collect, corev1alpha1.LogicalClusterPhaseReady, ws.Status.Phase, "workspace %s should still be Ready", ws.Name)
				got[ws.Name] = ws.UID
			}
		}
		require.Len(collect, got, len(before), "expected all pre-re-keying workspaces to be listed, got %v", got)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// requireShardsServeWorkspaces asserts the shards serve the Workspace
// objects under the given, unchanged tenancy identity, i.e. the drain moved
// the data and the identity-addressed wildcard path still finds it:
// wildcard-listing workspaces by identity straight from the shards, the way
// kcp's own informers do, must succeed on every shard and, across all
// shards, return the pre-re-keying workspaces with their UIDs. (Their
// Workspace objects live in the org workspace, which is on one shard only.)
func requireShardsServeWorkspaces(ctx context.Context, t *testing.T, shardConfigs map[string]*rest.Config, tenancyIdentity string, before map[string]types.UID) {
	t.Helper()
	clients := map[string]kubernetes.Interface{}
	for shard, cfg := range shardConfigs {
		client, err := kubernetes.NewForConfig(cfg)
		require.NoError(t, err)
		clients[shard] = client
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		found := map[string]types.UID{}
		for shard, client := range clients {
			list, err := listWorkspacesUnderIdentity(ctx, client, tenancyIdentity)
			require.NoError(collect, err, "shard %s should serve workspaces under identity %s", shard, tenancyIdentity)
			for _, ws := range list.Items {
				if uid, ok := before[ws.Name]; ok && uid == ws.UID {
					found[ws.Name] = ws.UID
				}
			}
		}
		require.Len(collect, found, len(before), "expected the shards to serve all pre-re-keying workspaces under identity %s, found %v", tenancyIdentity, found)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// listWorkspacesUnderIdentity lists all Workspace objects a shard serves
// under the given tenancy identity, straight from the shard with a wildcard
// request, the way kcp's own informers do.
func listWorkspacesUnderIdentity(ctx context.Context, client kubernetes.Interface, tenancyIdentity string) (*tenancyv1alpha1.WorkspaceList, error) {
	raw, err := client.Discovery().RESTClient().Get().
		AbsPath("/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:" + tenancyIdentity).
		Do(ctx).Raw()
	if err != nil {
		return nil, err
	}
	list := &tenancyv1alpha1.WorkspaceList{}
	if err := json.Unmarshal(raw, list); err != nil {
		return nil, fmt.Errorf("decoding workspace list: %w", err)
	}
	return list, nil
}

// describeShardProgress renders the per-shard drain reports for messages.
func describeShardProgress(entries []migrationv1alpha1.ShardMigrationProgress) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		key := e.StorageKey
		if len(key) > 8 {
			key = key[:8]
		}
		parts = append(parts, fmt.Sprintf("%s=%d/%d@%s(%s)", e.Shard, e.MigratedBindings, e.TotalBindings, key, e.LastUpdateTime.Format("15:04:05")))
	}
	return "[" + strings.Join(parts, " ") + "]"
}
