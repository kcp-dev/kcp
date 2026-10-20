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

package identityrotation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// coreRotationShards is the number of shards the test's own server runs:
// the rotated exports live in root, so with two shards the rotation
// controller, the migrators and the identity swap are exercised across
// shards and through the front-proxy.
const coreRotationShards = 2

// coreExports are the APIExports in the root workspace that serve kcp's own
// APIs. The first four are the ones whose identities the shards, the
// front-proxy and the virtual workspaces resolve at startup and inject into
// wildcard requests; migration.kcp.io also serves the rotation type itself.
var coreExports = []string{
	"migration.kcp.io",
	"shards.core.kcp.io",
	"topology.kcp.io",
	"tenancy.kcp.io",
	"cache.kcp.io",
}

// coreRotationRounds is how often every core export is rotated in a row.
const coreRotationRounds = 3

// coreRotationTimeout bounds one core export rotation. A tenancy.kcp.io
// rotation drains the tenancy binding of every workspace on every shard.
const coreRotationTimeout = 2 * time.Minute

// identityKeyFor is the identity key createIdentitySecret stores in the
// secret of the given name: distinct per secret, so every secret hashes to a
// distinct identity.
func identityKeyFor(secretName string) string {
	return "a-fresh-identity-key-for-" + secretName + "-0123456789abcdef"
}

// identityHashFor is the identity hash kcp derives from the secret
// createIdentitySecret creates under the given name.
func identityHashFor(secretName string) string {
	sum := sha256.Sum256([]byte(identityKeyFor(secretName)))
	return hex.EncodeToString(sum[:])
}

// TestCoreAPIExportIdentityRotation rotates the identities of kcp's core
// APIExports in the root workspace, all of them, three times in a row, and
// checks after every round that the platform still works: the workspaces
// created before the rotations are still there with their UIDs, new
// workspaces can be created on every shard and are reachable by path, and
// every shard serves the tenancy data under the new identity. The rotations
// are requested from an ops workspace that binds migration.kcp.io, which is
// itself one of the rotated exports. Completed rotations are deleted between
// rounds, which is what lifts the cooldown between rotations of one export.
//
// The test runs on a sharded server of its own: a tenancy.kcp.io rotation
// fences every workspace on every shard while it drains, which would disrupt
// any other test sharing the server.
//
//nolint:paralleltest // the test has a server of its own and runs in the serial suite, see framework.SerialSuite.
func TestCoreAPIExportIdentityRotation(t *testing.T) {
	framework.Suite(t, framework.SerialSuite)

	server := framework.PrivateShardedKcpServer(t, coreRotationShards)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := newRotationClients(t, server)
	shardNames := server.ShardNames()
	require.NotEmpty(t, shardNames, "the test server reports no shards")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	// the ops workspace goes to the last shard, a non-root shard whenever
	// there is one: the rotated exports live in root, so the rotation
	// controller has to reach them across shards.
	opsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("ops"), kcptesting.WithShard(shardNames[len(shardNames)-1]))
	bindMigrationAPI(ctx, t, c, opsPath)

	t.Logf("Create one consumer workspace per shard %v and a Partition; they must survive every rotation", shardNames)
	before := map[string]types.UID{}
	for _, shard := range shardNames {
		_, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer-%s", shard), kcptesting.WithShard(shard))
		before[ws.Name] = ws.UID
	}
	partition, err := c.kcp.Cluster(orgPath).TopologyV1alpha1().Partitions().Create(ctx, &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{Name: "survivor"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	for round := 1; round <= coreRotationRounds; round++ {
		for _, export := range coreExports {
			name := fmt.Sprintf("%s-round-%d", export, round)
			t.Logf("Round %d: rotate root:%s onto secret %s", round, export, name)

			createIdentitySecret(ctx, t, c, core.RootCluster.Path(), name)
			requestRotation(ctx, t, c, opsPath, migrationv1alpha1.ExportReference{Path: core.RootCluster.Path().String(), Name: export}, name, name,
				migrationv1alpha1.AliasRetirement{Policy: migrationv1alpha1.AliasRetirementImmediate})

			hash := waitForCoreRotationCompleted(ctx, t, c, opsPath, name, shardNames)
			require.Equal(t, identityHashFor(name), hash, "rotation %s computed an unexpected identity", name)
			requireCoreExportOnIdentity(ctx, t, c, export, hash)
			deleteCompletedRotation(ctx, t, c, opsPath, name)
		}

		t.Logf("Round %d: every core export is rotated; check the platform still works", round)
		requireWorkspacesSurvived(ctx, t, c, orgPath, before)
		got, err := c.kcp.Cluster(orgPath).TopologyV1alpha1().Partitions().Get(ctx, partition.Name, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, partition.UID, got.UID, "the Partition must survive the topology.kcp.io rotation with its UID")

		for _, shard := range shardNames {
			// a new workspace only becomes Ready when the shard's own wildcard
			// informers (workspaces, logicalclusters, workspacetypes) use the
			// new identities; its path only resolves when the front-proxy does.
			path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("round-%d-%s", round, shard), kcptesting.WithShard(shard))
			_, err := c.kube.Cluster(path).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "the new workspace %s should be reachable by path", path)
		}

		shardConfigs := map[string]*rest.Config{}
		for _, shard := range shardNames {
			shardConfigs[shard] = server.ShardSystemMasterBaseConfig(t, shard)
		}
		requireShardsServeWorkspaces(ctx, t, shardConfigs, identityHashFor(fmt.Sprintf("tenancy.kcp.io-round-%d", round)), before)
	}
}

// waitForCoreRotationCompleted waits for the rotation to complete and
// returns the new identity hash. Unlike waitForRotationPhase it does not
// check the binding counters: it checks that every shard reported, and
// leaves the data checks to the caller.
func waitForCoreRotationCompleted(ctx context.Context, t *testing.T, c rotationClients, opsPath logicalcluster.Path, name string, shardNames []string) string {
	t.Helper()
	var hash string
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rotation, err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportIdentityRotations().Get(ctx, name, metav1.GetOptions{})
		require.NoError(collect, err)
		require.NotEqual(collect, migrationv1alpha1.APIExportIdentityRotationFailed, rotation.Status.Phase, "rotation failed: %v", rotation.Status.Conditions)
		require.Equal(collect, migrationv1alpha1.APIExportIdentityRotationCompleted, rotation.Status.Phase,
			"rotation phase %s, migrated %d/%d, shards %s", rotation.Status.Phase, rotation.Status.MigratedBindings, rotation.Status.TotalBindings, describeShardProgress(rotation.Status.Shards))
		require.True(collect, conditions.IsTrue(rotation, migrationv1alpha1.IdentityRotationDrained), "expected Drained=True")
		require.True(collect, conditions.IsTrue(rotation, migrationv1alpha1.IdentityRotationAliasRetired), "expected AliasRetired=True")
		reported := sets.New[string]()
		for _, shard := range rotation.Status.Shards {
			reported.Insert(shard.Shard)
		}
		require.True(collect, reported.HasAll(shardNames...), "expected drain reports from shards %v, got %v", shardNames, sets.List(reported))
		require.NotEmpty(collect, rotation.Status.NewIdentityHash)
		hash = rotation.Status.NewIdentityHash
	}, coreRotationTimeout, 250*time.Millisecond, "waiting for rotation %s to complete", name)
	return hash
}

// requireCoreExportOnIdentity asserts the root APIExport serves the given
// identity, has no alias left and is valid.
func requireCoreExportOnIdentity(ctx context.Context, t *testing.T, c rotationClients, export, hash string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		got, err := c.kcp.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, export, metav1.GetOptions{})
		require.NoError(collect, err)
		require.Equal(collect, hash, got.Status.IdentityHash, "root:%s should serve the new identity", export)
		require.Empty(collect, got.Status.IdentityAliasHashes, "root:%s should have no alias left", export)
		require.True(collect, conditions.IsTrue(got, apisv1alpha2.APIExportIdentityValid), "root:%s should be IdentityValid: %v", export, got.Status.Conditions)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// deleteCompletedRotation deletes the rotation, which lifts the cooldown the
// admission plugin keeps between completed rotations of the same export.
// Right after a migration.kcp.io rotation the rotation API itself may be
// briefly unavailable while its bound CRD is rebuilt, hence the retry.
func deleteCompletedRotation(ctx context.Context, t *testing.T, c rotationClients, opsPath logicalcluster.Path, name string) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := c.kcp.Cluster(opsPath).MigrationV1alpha1().APIExportIdentityRotations().Delete(ctx, name, metav1.DeleteOptions{})
		if err == nil || apierrors.IsNotFound(err) {
			return true, ""
		}
		return false, fmt.Sprintf("deleting rotation %s: %v", name, err)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// requireWorkspacesSurvived asserts the workspaces created before the
// rotations are still listed with their UIDs, and Ready.
func requireWorkspacesSurvived(ctx context.Context, t *testing.T, c rotationClients, orgPath logicalcluster.Path, before map[string]types.UID) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		list, err := c.kcp.Cluster(orgPath).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		require.NoError(collect, err)
		got := map[string]types.UID{}
		for _, ws := range list.Items {
			if uid, ok := before[ws.Name]; ok {
				require.Equal(collect, uid, ws.UID, "workspace %s must survive the rotations with its UID", ws.Name)
				require.Equal(collect, corev1alpha1.LogicalClusterPhaseReady, ws.Status.Phase, "workspace %s should still be Ready", ws.Name)
				got[ws.Name] = ws.UID
			}
		}
		require.Len(collect, got, len(before), "expected all pre-rotation workspaces to be listed, got %v", got)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// requireShardsServeWorkspaces asserts the shards serve the Workspace
// objects under the given tenancy identity, i.e. the drain moved the data to
// the new storage prefix: wildcard-listing workspaces under the new identity
// straight from the shards, the way kcp's own informers do, must succeed on
// every shard and, across all shards, return the pre-rotation workspaces
// with their UIDs. (Their Workspace objects live in the org workspace, which
// is on one shard only.)
func requireShardsServeWorkspaces(ctx context.Context, t *testing.T, shardConfigs map[string]*rest.Config, tenancyHash string, before map[string]types.UID) {
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
			list, err := listWorkspacesUnderIdentity(ctx, client, tenancyHash)
			require.NoError(collect, err, "shard %s should serve workspaces under identity %s", shard, tenancyHash)
			for _, ws := range list.Items {
				if uid, ok := before[ws.Name]; ok && uid == ws.UID {
					found[ws.Name] = ws.UID
				}
			}
		}
		require.Len(collect, found, len(before), "expected the shards to serve all pre-rotation workspaces under identity %s, found %v", tenancyHash, found)
	}, wait.ForeverTestTimeout, 250*time.Millisecond)
}

// listWorkspacesUnderIdentity lists all Workspace objects a shard serves
// under the given tenancy identity, straight from the shard with a wildcard
// request, the way kcp's own informers do.
func listWorkspacesUnderIdentity(ctx context.Context, client kubernetes.Interface, tenancyHash string) (*tenancyv1alpha1.WorkspaceList, error) {
	raw, err := client.Discovery().RESTClient().Get().
		AbsPath("/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:" + tenancyHash).
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
		hash := e.IdentityHash
		if len(hash) > 8 {
			hash = hash[:8]
		}
		parts = append(parts, fmt.Sprintf("%s=%d/%d@%s(%s)", e.Shard, e.MigratedBindings, e.TotalBindings, hash, e.LastUpdateTime.Format("15:04:05")))
	}
	return "[" + strings.Join(parts, " ") + "]"
}
