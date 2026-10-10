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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestMigrationDisconnectsWildcardWatches migrates the consumer holding the
// data, not the APIExport provider. Existing watches on both shards must close
// instead of reporting the copy and cleanup as object creations and deletions.
//
//nolint:paralleltest // Watch closure subtests must finish before the parent checks migration completion.
func TestMigrationDisconnectsWildcardWatches(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := privateMigrationServer(t)
	shards := server.ShardNames()

	origin, destination := shards[0], shards[1]

	cfg := server.BaseConfig(t)
	kcpClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Creating provider and consumer workspaces")
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumer := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(origin))

	// A second binding keeps the export served on the destination even before
	// the migrating consumer arrives, so its pre-migration watch is real.
	stationaryPath, stationary := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithShard(destination))

	t.Log("Exporting ConfigMaps and binding the export in both consumers")
	claim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"},
		Verbs:         []string{"get", "list", "watch"},
	}
	_, err = kcpClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "cm-watcher"},
		Spec:       apisv1alpha2.APIExportSpec{PermissionClaims: []apisv1alpha2.PermissionClaim{claim}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	claims := []apisv1alpha2.AcceptablePermissionClaim{{
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: claim,
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		},
		State: apisv1alpha2.ClaimAccepted,
	}}
	bindExportForWildcardWatchTest(t, kcpClient, consumerPath, "cm-watcher", providerPath, claims)
	bindExportForWildcardWatchTest(t, kcpClient, stationaryPath, "cm-watcher", providerPath, claims)
	bindExportForWildcardWatchTest(t, kcpClient, orgPath, "migration.kcp.io", core.RootCluster.Path(), nil)

	t.Log("Creating the ConfigMap to migrate")
	selector := "migration-watch-test=true"
	createCM := func(path logicalcluster.Path, name string) {
		t.Helper()
		_, err := kubeClient.Cluster(path).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"migration-watch-test": "true"},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	createCM(consumerPath, "migrating-cm")

	t.Log("Establishing a live wildcard watch on each shard")
	watchCases := []struct {
		shard         string
		workspace     *tenancyv1alpha1.Workspace
		path          logicalcluster.Path
		initialCount  int
		migratedCount int
		client        kcpkubernetesclientset.ClusterInterface
		watcher       watch.Interface
	}{
		{
			shard:         origin,
			workspace:     consumer,
			path:          consumerPath,
			initialCount:  1,
			migratedCount: 0,
		},
		{
			shard:         destination,
			workspace:     stationary,
			path:          stationaryPath,
			initialCount:  0,
			migratedCount: 2, // The migrating ConfigMap and its watch probe.
		},
	}
	for i := range watchCases {
		tc := &watchCases[i]
		vwCfg := rest.CopyConfig(cfg)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			slice, err := kcpClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), "cm-watcher", metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			var found bool
			vwCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), cfg, tc.workspace, framework.ExportVirtualWorkspaceURLs(slice))
			return err == nil && found, fmt.Sprintf("waiting for virtual workspace on shard %s: %v", tc.shard, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		tc.client, err = kcpkubernetesclientset.NewForConfig(vwCfg)
		require.NoError(t, err)

		var rv string
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := tc.client.CoreV1().ConfigMaps().List(t.Context(), metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return false, err.Error()
			}
			rv = list.ResourceVersion
			return len(list.Items) == tc.initialCount, fmt.Sprintf("want %d configmaps, got %d", tc.initialCount, len(list.Items))
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		tc.watcher, err = tc.client.CoreV1().ConfigMaps().Watch(t.Context(), metav1.ListOptions{ResourceVersion: rv, LabelSelector: selector})
		require.NoError(t, err)
		t.Cleanup(tc.watcher.Stop)

		// An actual event proves the watch is established before migration.
		createCM(tc.path, "watch-probe")
		select {
		case event, ok := <-tc.watcher.ResultChan():
			require.True(t, ok, "watch on %s closed before migration", tc.shard)
			require.Equal(t, watch.Added, event.Type)
			cm, ok := event.Object.(*corev1.ConfigMap)
			require.True(t, ok)
			require.Equal(t, "watch-probe", cm.Name)
		case <-time.After(wait.ForeverTestTimeout):
			t.Fatalf("watch on %s was not established", tc.shard)
		}
	}

	t.Log("Migrating the consumer to the destination shard")
	_, err = kcpClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "wildcard-watch"},
		Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
			LogicalCluster:   consumer.Spec.Cluster,
			DestinationShard: destination,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Verifying both watches close without migration data events")
	for _, tc := range watchCases {
		t.Run(tc.shard, func(t *testing.T) {
			deadline := time.NewTimer(wait.ForeverTestTimeout)
			defer deadline.Stop()
			for {
				select {
				case event, ok := <-tc.watcher.ResultChan():
					if !ok {
						return
					}
					// An error may precede closure. No data event should be
					// produced by copying or cleaning up our selected objects.
					require.Equal(t, watch.Error, event.Type, "migration leaked an event: %#v", event.Object)
				case <-deadline.C:
					t.Fatal("wildcard watch stayed open during migration")
				}
			}
		})
	}

	t.Log("Waiting for migration to complete")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		migration, err := kcpClient.Cluster(orgPath).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), "wildcard-watch", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return migration.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, fmt.Sprintf("migration phase: %s", migration.Status.Phase)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Verifying the migrated ConfigMaps are visible only on the destination")
	for _, tc := range watchCases {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := tc.client.CoreV1().ConfigMaps().List(t.Context(), metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return false, err.Error()
			}
			count := 0
			for _, cm := range list.Items {
				if logicalcluster.From(&cm).String() == consumer.Spec.Cluster {
					count++
				}
			}
			return count == tc.migratedCount, fmt.Sprintf("shard %s: want %d migrated configmaps, got %d", tc.shard, tc.migratedCount, count)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}
}

func bindExportForWildcardWatchTest(
	t *testing.T,
	kcpClient kcpclientset.ClusterInterface,
	path logicalcluster.Path,
	name string,
	exportPath logicalcluster.Path,
	claims []apisv1alpha2.AcceptablePermissionClaim,
) {
	t.Helper()

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClient.Cluster(path).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{
						Path: exportPath.String(),
						Name: name,
					},
				},
				PermissionClaims: claims,
			},
		}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("waiting to bind export %s: %v", name, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClient.Cluster(path).ApisV1alpha2().APIBindings().Get(t.Context(), name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound, "waiting for binding " + name
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
