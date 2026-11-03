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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
)

func cancellationMigration(phase migrationv1alpha1.LogicalClusterMigrationPhaseType) *migrationv1alpha1.LogicalClusterMigration {
	now := metav1.Now()
	return &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Annotations: map[string]string{logicalcluster.AnnotationKey: "org"}, DeletionTimestamp: &now, Finalizers: []string{MigrationFinalizer}},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: "consumer", DestinationShard: "destination"},
		Status:     migrationv1alpha1.LogicalClusterMigrationStatus{OriginShard: "origin", Phase: phase, DumpContinue: "forward-token"},
	}
}

func cancellationController(shard string) *Controller {
	return &Controller{
		shardName:                       shard,
		migratingLogicalClusters:        NewMigratingLogicalClusters(),
		ddsif:                           newEmptyDDSIF(),
		cancelLogicalClusterConnections: func(logicalcluster.Path, error) {},
		deleteLogicalClusterContext:     func(logicalcluster.Path, error) {},
	}
}

func TestCancellationHandoff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		phase, next migrationv1alpha1.LogicalClusterMigrationPhaseType
		owner       string
	}{
		{"", "CancellationFinalize", "origin"},
		{"Preparing", "CancellationFinalize", "origin"},
		{"Migrating", "CancellationCleanup", "destination"},
		{"OriginCleanup", "CancellationPreparing", "origin"},
		{"DestinationFinalize", "CancellationPreparing", "destination"},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			t.Parallel()
			m := cancellationMigration(tc.phase)
			other := "origin"
			if tc.owner == other {
				other = "destination"
			}
			requeue, err := cancellationController(other).reconcile(t.Context(), m)
			require.NoError(t, err)
			_ = requeue
			require.Equal(t, tc.phase, m.Status.Phase, "the idle shard cannot race the active shard's writes")
			_, err = cancellationController(tc.owner).reconcile(t.Context(), m)
			require.NoError(t, err)
			require.Equal(t, tc.next, m.Status.Phase)
			require.Empty(t, m.Status.DumpContinue)
			require.Contains(t, m.Finalizers, MigrationFinalizer)
		})
	}
}

func TestCancellationRestoreResumesAndRetainsDestinationOnFailure(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup)
	c := cancellationController("origin")
	require.True(t, c.startCancellation(m))
	// The destination has acknowledged that it is frozen and can serve dumps.
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseRestoring
	c.copyPageFromOrigin = func(_ context.Context, lc logicalcluster.Name, shard, token string) (int64, string, error) {
		require.Equal(t, logicalcluster.Name("consumer"), lc)
		require.Equal(t, "destination", shard)
		require.Empty(t, token, "rollback must restart the scan from the beginning")
		return 10, "reverse-token", nil
	}
	_, err := c.reconcile(t.Context(), m)
	require.NoError(t, err)
	require.Equal(t, "reverse-token", m.Status.DumpContinue)
	// Simulate a controller restart after the page checkpoint was persisted.
	c = cancellationController("origin")
	c.copyPageFromOrigin = func(_ context.Context, _ logicalcluster.Name, _, token string) (int64, string, error) {
		require.Equal(t, "reverse-token", token)
		return 0, "", errors.New("destination temporarily unavailable")
	}
	_, err = c.reconcile(t.Context(), m)
	require.Error(t, err)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseRestoring, m.Status.Phase)
	require.Equal(t, "reverse-token", m.Status.DumpContinue)
	require.Contains(t, m.Finalizers, MigrationFinalizer)
	c.copyPageFromOrigin = func(context.Context, logicalcluster.Name, string, string) (int64, string, error) { return 2, "", nil }
	_, err = c.reconcile(t.Context(), m)
	require.NoError(t, err)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup, m.Status.Phase)
}

type cancellationKV struct {
	*fakeKV
	err     error
	deleted []string
}

func (kv *cancellationKV) Delete(_ context.Context, key string, _ ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	if kv.err != nil {
		return nil, kv.err
	}
	kv.deleted = append(kv.deleted, key)
	return &clientv3.DeleteResponse{}, nil
}

func TestCancellationCleanupFailureRetainsFinalizer(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup)
	kv := &cancellationKV{fakeKV: newFakeKV(map[string]string{
		"/registry/core/configmaps/consumer/default/partial": "value",
		"/registry/core/configmaps/other/default/untouched":  "value",
	}), err: errors.New("etcd unavailable")}
	c := cancellationController("destination")
	c.etcdStoragePrefix = "/registry"
	c.etcdClient = &clientv3.Client{KV: kv}
	_, err := c.reconcile(t.Context(), m)
	require.Error(t, err)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup, m.Status.Phase)
	require.True(t, c.migratingLogicalClusters.IsMigrating("consumer"))
	require.Contains(t, m.Finalizers, MigrationFinalizer)
	kv.err = nil
	_, err = c.reconcile(t.Context(), m)
	require.NoError(t, err)
	require.Equal(t, []string{"/registry/core/configmaps/consumer"}, kv.deleted)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize, m.Status.Phase)
	require.False(t, c.migratingLogicalClusters.IsMigrating("consumer"))
}

func TestCancellationFinalize(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"org:move", "org:other"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize)
			lc := &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster", Annotations: map[string]string{logicalcluster.AnnotationKey: "consumer", MigratingAnnotationKey: owner}}}
			// The generated field-managed fake lacks the LogicalCluster schema.
			client := kcpfake.NewSimpleClientset(lc) //nolint:staticcheck
			c := cancellationController("origin")
			c.localKcpClusterClient = client
			require.NoError(t, c.migratingLogicalClusters.Set("consumer", owner))
			_, err := c.reconcile(t.Context(), m)
			require.NoError(t, err)
			got, err := client.Cluster(logicalcluster.NewPath("consumer")).CoreV1alpha1().LogicalClusters().Get(t.Context(), "cluster", metav1.GetOptions{})
			require.NoError(t, err)
			if strings.HasSuffix(owner, "other") {
				require.Equal(t, owner, got.Annotations[MigratingAnnotationKey])
				require.True(t, c.migratingLogicalClusters.IsMigrating("consumer"))
			} else {
				require.Empty(t, got.Annotations[MigratingAnnotationKey])
				require.False(t, c.migratingLogicalClusters.IsMigrating("consumer"))
			}
			require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancelled, m.Status.Phase)
			require.True(t, conditions.IsTrue(m, migrationv1alpha1.LCMigrationCancelled))
			require.Contains(t, m.Finalizers, MigrationFinalizer, "terminal status must be persisted first")
			_, err = c.reconcile(t.Context(), m)
			require.NoError(t, err)
			require.NotContains(t, m.Finalizers, MigrationFinalizer)
		})
	}
}

func TestCancellationDoesNotReleaseMissingRestoredData(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize)
	conditions.MarkTrue(m, migrationv1alpha1.LCMigrationOriginReady)
	conditions.MarkTrue(m, migrationv1alpha1.LCMigrationDataCopied)
	c := cancellationController("origin")
	c.localKcpClusterClient = kcpfake.NewClientset()
	_, err := c.reconcile(t.Context(), m)
	require.Error(t, err)
	require.Contains(t, m.Finalizers, MigrationFinalizer)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize, m.Status.Phase)
}

func TestCancellationOfDeletedWorkspaceBeforeCopyCompletes(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize)
	conditions.MarkTrue(m, migrationv1alpha1.LCMigrationOriginReady)
	c := cancellationController("origin")
	c.localKcpClusterClient = kcpfake.NewClientset()
	require.NoError(t, c.migratingLogicalClusters.Set("consumer", "org:move"))
	_, err := c.reconcile(t.Context(), m)
	require.NoError(t, err)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancelled, m.Status.Phase)
	require.False(t, c.migratingLogicalClusters.IsMigrating("consumer"))
	_, err = c.reconcile(t.Context(), m)
	require.NoError(t, err)
	require.NotContains(t, m.Finalizers, MigrationFinalizer)
}

func TestDeletingCompletedMigrationDoesNotRollback(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCompleted)
	_, err := cancellationController("destination").reconcile(t.Context(), m)
	require.NoError(t, err)
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCompleted, m.Status.Phase)
	require.NotContains(t, m.Finalizers, MigrationFinalizer)
}

type cancellationQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	key   string
	delay time.Duration
}

func (q *cancellationQueue) AddAfter(key string, delay time.Duration) {
	q.key, q.delay = key, delay
}

func TestCancellationPollsWhileWaitingForOtherShard(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseCancellationPreparing)
	c := cancellationController("origin")
	c.kcpClusterClient = kcpfake.NewClientset(m)
	q := &cancellationQueue{}
	c.queue = q
	c.commit = func(context.Context, *logicalClusterMigrationResource, *logicalClusterMigrationResource) error {
		return errors.New("checkpoint failed")
	}
	_, err := c.process(t.Context(), "org|move")
	require.Error(t, err)
	require.Empty(t, q.key, "even an idle shard must respect error backoff")
	c.commit = func(context.Context, *logicalClusterMigrationResource, *logicalClusterMigrationResource) error {
		return nil
	}
	requeue, err := c.process(t.Context(), "org|move")
	require.NoError(t, err)
	require.False(t, requeue, "waiting must not busy-loop")
	require.Equal(t, "org|move", q.key)
	require.Equal(t, time.Second, q.delay)
}

func TestMigrationPollsBeforeCancellationIsObserved(t *testing.T) {
	t.Parallel()
	m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhasePreparing)
	m.DeletionTimestamp = nil
	// The generated field-managed fake lacks the migration schema for Update.
	client := kcpfake.NewSimpleClientset(m) //nolint:staticcheck
	c := cancellationController("destination")
	c.kcpClusterClient = client
	q := &cancellationQueue{}
	c.queue = q
	c.commit = func(context.Context, *logicalClusterMigrationResource, *logicalClusterMigrationResource) error {
		return nil
	}
	requeue, err := c.process(t.Context(), "org|move")
	require.NoError(t, err)
	require.False(t, requeue)
	require.Equal(t, "org|move", q.key, "the idle destination must poll before DELETE can be missed by replication")

	// Change only the authoritative object. No cache event is delivered.
	m.DeletionTimestamp = cancellationMigration("").DeletionTimestamp
	m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseCancellationCleanup
	_, err = client.Cluster(logicalcluster.NewPath("org")).MigrationV1alpha1().LogicalClusterMigrations().Update(t.Context(), m, metav1.UpdateOptions{})
	require.NoError(t, err)
	kv := &cancellationKV{fakeKV: newFakeKV(map[string]string{})}
	c.etcdStoragePrefix = "/registry"
	c.etcdClient = &clientv3.Client{KV: kv}
	c.commit = func(_ context.Context, _, next *logicalClusterMigrationResource) error {
		require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseCancellationFinalize, next.Status.Phase)
		return nil
	}
	requeue, err = c.process(t.Context(), q.key)
	require.NoError(t, err)
	require.True(t, requeue, "the scheduled poll must advance the cancellation handoff")
}

func TestCancellationRestoreScheduling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		copyError, commitError bool
	}{
		{name: "next page"},
		{name: "copy failure", copyError: true},
		{name: "checkpoint failure", commitError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := cancellationMigration(migrationv1alpha1.LogicalClusterMigrationPhaseRestoring)
			c := cancellationController("origin")
			c.kcpClusterClient = kcpfake.NewClientset(m)
			q := &cancellationQueue{}
			c.queue = q
			c.copyPageFromOrigin = func(context.Context, logicalcluster.Name, string, string) (int64, string, error) {
				if tc.copyError {
					return 0, "", errors.New("copy failed")
				}
				return 1, "next-page", nil
			}
			c.commit = func(context.Context, *logicalClusterMigrationResource, *logicalClusterMigrationResource) error {
				if tc.commitError {
					return errors.New("checkpoint failed")
				}
				return nil
			}
			requeue, err := c.process(t.Context(), "org|move")
			if tc.copyError || tc.commitError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.True(t, requeue, "restore pages must progress immediately")
			}
			require.Empty(t, q.key, "polling must not throttle progress or bypass error backoff")
		})
	}
}
