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
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Hold a real dump request in flight so cancellation does not depend on winning
// a race against a small workspace's migration. Releasing it after deletion also
// checks that a stale forward-copy reconcile cannot bypass cancellation.
func TestMigrationCancellationDuringCopy(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	var proxy *httputil.ReverseProxy
	proxyReady := make(chan struct{})
	entered, release := make(chan struct{}), make(chan struct{})
	dumped := make(chan int, 1)
	var enteredOnce, releaseOnce, dumpedOnce sync.Once
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-proxyReady:
		case <-r.Context().Done():
			return
		}
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, r)
			for key, values := range response.Header() {
				w.Header()[key] = values
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			dumpedOnce.Do(func() { dumped <- response.Code })
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(gate.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	server := privateMigrationServer(t, "--shard-virtual-workspace-url="+gate.URL)
	cfg := server.BaseConfig(t)
	client, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kube, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	org, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithShard("root"))
	path, ws := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithShard("root"))
	cm, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "preserved"}, Data: map[string]string{"value": "original"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	bindExportForWildcardWatchTest(t, client, org, "migration.kcp.io", core.RootCluster.Path(), nil)

	shards := client.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards()
	origin, err := shards.Get(t.Context(), "root", metav1.GetOptions{})
	require.NoError(t, err)
	target, err := url.Parse(origin.Spec.BaseURL)
	require.NoError(t, err)
	dumpConfig := rest.CopyConfig(cfg)
	dumpConfig.Impersonate = rest.ImpersonationConfig{UserName: "cancellation-test", Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}}
	transport, err := rest.TransportFor(dumpConfig)
	require.NoError(t, err)
	proxy = httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	close(proxyReady)
	migrations := client.Cluster(org).MigrationV1alpha1().LogicalClusterMigrations()
	_, err = migrations.Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "cancel-copy"},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: ws.Spec.Cluster, DestinationShard: "shard-1"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("destination did not request the gated dump")
	}
	require.NoError(t, migrations.Delete(t.Context(), "cancel-copy", metav1.DeleteOptions{}))
	pending, err := migrations.Get(t.Context(), "cancel-copy", metav1.GetOptions{})
	require.NoError(t, err, "the finalizer must retain the migration while a copy is in flight")
	require.NotNil(t, pending.DeletionTimestamp)
	releaseOnce.Do(func() { close(release) })
	select {
	case code := <-dumped:
		require.Equal(t, http.StatusCreated, code, "the in-flight dump must succeed before cancellation cleans its copied data")
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("in-flight dump did not finish")
	}
	require.Eventually(t, func() bool {
		_, err := migrations.Get(t.Context(), "cancel-copy", metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "cancellation must release its finalizer")
	require.Eventually(t, func() bool {
		got, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		return err == nil && got.UID == cm.UID && got.Data["value"] == "original"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "workspace must be available with its original data")

	// Read both shards directly: the origin must retain the object and the
	// destination must have removed even the page written after deletion.
	for _, name := range []string{"root", "shard-1"} {
		shard, err := shards.Get(t.Context(), name, metav1.GetOptions{})
		require.NoError(t, err)
		local := rest.CopyConfig(cfg)
		local.Host = shard.Spec.BaseURL
		localClient, err := kcpkubernetesclientset.NewForConfig(local)
		require.NoError(t, err)
		got, err := localClient.Cluster(logicalcluster.NewPath(ws.Spec.Cluster)).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		if name == "root" {
			require.NoError(t, err)
			require.Equal(t, cm.UID, got.UID)
		} else {
			require.True(t, apierrors.IsNotFound(err), "destination still serves migrated data: %v", err)
		}
	}
}

// Reconstruct the persisted state after origin cleanup. This avoids racing a
// DELETE against the very short cleanup phase: a completed forward migration
// supplies the real destination dataset and an empty source, and an extra test
// finalizer lets us restore the crash checkpoint after requesting deletion.
func TestMigrationCancellationRestoresDeletedOrigin(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	server, restart := privateMigrationServerWithRestart(t, nil)
	cfg := server.BaseConfig(t)
	client, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kube, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	org, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithShard("root"))
	path, ws := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithShard("root"))
	cm, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-me"}, Data: map[string]string{"value": "original"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	bindExportForWildcardWatchTest(t, client, org, "migration.kcp.io", core.RootCluster.Path(), nil)
	migrations := client.Cluster(org).MigrationV1alpha1().LogicalClusterMigrations()
	const hold = "test.kcp.io/cancellation-checkpoint"
	_, err = migrations.Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "restore-origin", Finalizers: []string{hold}},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: ws.Spec.Cluster, DestinationShard: "shard-1"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m, err := migrations.Get(t.Context(), "restore-origin", metav1.GetOptions{})
		return err == nil && m.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCompleted && len(m.Finalizers) == 1 && m.Finalizers[0] == hold
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.NoError(t, migrations.Delete(t.Context(), "restore-origin", metav1.DeleteOptions{}))
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		m, err := migrations.Get(t.Context(), "restore-origin", metav1.GetOptions{})
		if err != nil {
			return err
		}
		m.Status.Phase = migrationv1alpha1.LogicalClusterMigrationPhaseOriginCleanup
		_, err = migrations.UpdateStatus(t.Context(), m, metav1.UpdateOptions{})
		return err
	}))
	// Restart to enqueue the checkpoint: completed migrations no longer poll,
	// and replication does not forward updates to finalizing objects.
	restart("root")
	restart("shard-1")
	require.Eventually(t, func() bool {
		m, err := migrations.Get(t.Context(), "restore-origin", metav1.GetOptions{})
		return err == nil && m.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCancelled
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "rollback must copy the destination dataset back before cleaning it")
	require.Eventually(t, func() bool {
		got, err := kube.Cluster(path).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		return err == nil && got.UID == cm.UID && got.Data["value"] == "original"
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	for _, name := range []string{"root", "shard-1"} {
		shard, err := client.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Get(t.Context(), name, metav1.GetOptions{})
		require.NoError(t, err)
		local := rest.CopyConfig(cfg)
		local.Host = shard.Spec.BaseURL
		localClient, err := kcpkubernetesclientset.NewForConfig(local)
		require.NoError(t, err)
		got, err := localClient.Cluster(logicalcluster.NewPath(ws.Spec.Cluster)).CoreV1().ConfigMaps("default").Get(t.Context(), cm.Name, metav1.GetOptions{})
		if name == "root" {
			require.NoError(t, err)
			require.Equal(t, cm.UID, got.UID)
			require.Equal(t, cm.Data, got.Data)
		} else {
			require.True(t, apierrors.IsNotFound(err), "destination copy should be removed: %v", err)
		}
	}
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		m, err := migrations.Get(t.Context(), "restore-origin", metav1.GetOptions{})
		if err != nil {
			return err
		}
		m.Finalizers = nil
		_, err = migrations.Update(t.Context(), m, metav1.UpdateOptions{})
		return err
	}))
}
