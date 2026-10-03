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

package logicalclustermigration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestMigrationBackToOriginShard verifies that a cluster remains accessible
// after migrating away from and back to its original shard.
func TestMigrationBackToOriginShard(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("requires multi-shard setup")
	}

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	shardA := server.ShardNames()[0]
	shardB := server.ShardNames()[1]

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	wsPath, ws := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(shardA))
	lcName := logicalcluster.Name(ws.Spec.Cluster)

	t.Logf("Workspace %s (logical cluster %s) created on shard %s", wsPath, lcName, shardA)

	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Create(
		t.Context(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "pre-migration-cm"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	bindMigrationAPI(t, kcpClusterClient, orgPath)

	migrate := func(name, destinationShard string) {
		t.Helper()

		t.Logf("Creating LogicalClusterMigration %q for %s to %s", name, lcName, destinationShard)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(
				t.Context(),
				&migrationv1alpha1.LogicalClusterMigration{
					ObjectMeta: metav1.ObjectMeta{Name: name},
					Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
						LogicalCluster:   lcName.String(),
						DestinationShard: destinationShard,
					},
				},
				metav1.CreateOptions{},
			)
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create LogicalClusterMigration %q", name)

		t.Logf("Waiting for migration %q to complete", name)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			migration, err := kcpClusterClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), name, metav1.GetOptions{})
			require.NoError(c, err)
			t.Logf("migration %q phase: %q, conditions: %v", name, migration.Status.Phase, conditionsSummary(migration.Status.Conditions))
			require.Equal(c, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, migration.Status.Phase)
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "migration %q did not complete", name)
	}

	requireClientAccess := func(cmName string) {
		t.Helper()

		cmClient := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default")
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := cmClient.Get(t.Context(), "pre-migration-cm", metav1.GetOptions{})
			require.NoError(c, err, "GET should succeed after migration")
		}, wait.ForeverTestTimeout, 500*time.Millisecond, "logical cluster not accessible after migration")

		_, err := cmClient.Create(t.Context(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: cmName}}, metav1.CreateOptions{})
		require.NoError(t, err, "CREATE should succeed after migration")

		cms, err := cmClient.List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err, "LIST should succeed after migration")
		names := make([]string, 0, len(cms.Items))
		for _, cm := range cms.Items {
			names = append(names, cm.Name)
		}
		require.Contains(t, names, cmName, "ConfigMap should be listed after migration")
	}

	migrate("to-shard-b", shardB)
	requireClientAccess("after-first-migration-cm")

	migrate("back-to-shard-a", shardA)
	requireClientAccess("after-second-migration-cm")
}
