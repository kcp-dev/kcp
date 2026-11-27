/*
Copyright 2025 The kcp Authors.

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

package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func setInactiveAnnotation(t *testing.T, kcpClient kcpclientset.ClusterInterface, orgPath logicalcluster.Path, value bool) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		lc, err := kcpClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().Get(t.Context(), "cluster", v1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if value {
			lc.Annotations[corev1alpha1.LogicalClusterInactiveAnnotationKey] = "true"
		} else {
			delete(lc.Annotations, corev1alpha1.LogicalClusterInactiveAnnotationKey)
		}
		_, err = kcpClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().Update(t.Context(), lc, v1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

func drainAndExpectClose(t *testing.T, w watch.Interface) {
	t.Helper()
	for {
		select {
		case _, ok := <-w.ResultChan():
			if !ok {
				return
			}
		case <-time.After(wait.ForeverTestTimeout):
			t.Fatal("watch was not terminated after marking logical cluster inactive")
		}
	}
}

// TestInactiveLogicalClusterBlocksRequests checks that requests made
// against a logical cluster marked as inactive are failing and succeed
// after the inactive annotation is no longer "true".
func TestInactiveLogicalClusterBlocksRequests(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.SerialSuite)

	// A separate server keeps a regression in the handling of inactive
	// logical clusters from affecting other tests.
	server := kcptesting.PrivateKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// we need system identity here for toggling inactive annotation
	kcpClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Mark logical cluster inactive")
	setInactiveAnnotation(t, kcpClient, orgPath, true)

	t.Log("Verify that normal requests fail")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClient.Cluster(orgPath).CoreV1().Namespaces().List(t.Context(), v1.ListOptions{})
		if err == nil {
			return false, "expected error when accessing an inactive logical cluster"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Remove inactive annotation again")
	setInactiveAnnotation(t, kcpClient, orgPath, false)

	t.Log("Verify that normal requests succeed again")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClient.Cluster(orgPath).CoreV1().Namespaces().List(t.Context(), v1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

// TestInactiveLogicalClusterTerminatesClusterScopedWatch verifies that
// a cluster-scoped watch is cancelled when the cluster is marked as
// inactive and can be reestablished after the annotation is no longer
// "true".
func TestInactiveLogicalClusterTerminatesClusterScopedWatch(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.SerialSuite)

	// A separate server keeps a regression in the handling of inactive
	// logical clusters from affecting other tests.
	server := kcptesting.PrivateKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kcpClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Opening watch on ConfigMaps before marking inactive")
	watcher, err := kubeClient.Cluster(orgPath).CoreV1().ConfigMaps("default").Watch(t.Context(), v1.ListOptions{})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)

	t.Log("Mark logical cluster inactive")
	setInactiveAnnotation(t, kcpClient, orgPath, true)

	t.Log("Verify that the open watch is terminated")
	drainAndExpectClose(t, watcher)

	t.Log("Remove inactive annotation again")
	setInactiveAnnotation(t, kcpClient, orgPath, false)

	t.Log("Verify that a new watch can be opened after re-activation")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		newWatcher, err := kubeClient.Cluster(orgPath).CoreV1().ConfigMaps("default").Watch(t.Context(), v1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		t.Cleanup(newWatcher.Stop)
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

// TestInactiveLogicalClustersKeepWildcardWatchOpen marks several logical
// clusters inactive one after another, as a bulk suspension of tenants
// would, and verifies that an open wildcard watch survives and keeps
// delivering events from logical clusters that are still active.
//
// Wildcard requests are not blocked for inactive logical clusters, so
// terminating them would only make every wildcard client on the shard
// reconnect once per inactivated logical cluster, see
// https://github.com/kcp-dev/kcp/issues/4398.
func TestInactiveLogicalClustersKeepWildcardWatchOpen(t *testing.T) {
	t.Parallel()
	framework.Suite(t, framework.SerialSuite)

	// A separate server keeps a regression from resetting the wildcard
	// watches of other tests.
	server := kcptesting.PrivateKcpServer(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	activePath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	const inactiveCount = 3
	inactivePaths := make([]logicalcluster.Path, 0, inactiveCount)
	for range inactiveCount {
		path, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
		inactivePaths = append(inactivePaths, path)
	}

	// we need system identity here for toggling inactive annotation
	systemCfg := server.RootShardSystemMasterBaseConfig(t)

	kcpClient, err := kcpclientset.NewForConfig(systemCfg)
	require.NoError(t, err)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(systemCfg)
	require.NoError(t, err)

	// The label selector limits the wildcard watch to the probes created
	// by this test.
	const probeLabel = "inactive-wildcard-watch-test"
	selector := probeLabel + "=true"
	createProbe := func(name string) {
		t.Helper()
		_, err := kubeClient.Cluster(activePath).CoreV1().ConfigMaps("default").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: v1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{probeLabel: "true"},
			},
		}, v1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Log("Opening wildcard watch on ConfigMaps before marking logical clusters inactive")
	list, err := kubeClient.CoreV1().ConfigMaps().List(t.Context(), v1.ListOptions{LabelSelector: selector})
	require.NoError(t, err)
	watcher, err := kubeClient.CoreV1().ConfigMaps().Watch(t.Context(), v1.ListOptions{ResourceVersion: list.ResourceVersion, LabelSelector: selector})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)

	expectProbe := func(name string) {
		t.Helper()
		select {
		case event, ok := <-watcher.ResultChan():
			require.True(t, ok, "wildcard watch was terminated")
			require.Equal(t, watch.Added, event.Type)
			cm, ok := event.Object.(*corev1.ConfigMap)
			require.True(t, ok, "unexpected object %T", event.Object)
			require.Equal(t, name, cm.Name)
		case <-time.After(wait.ForeverTestTimeout):
			t.Fatalf("wildcard watch did not deliver the creation of ConfigMap %s", name)
		}
	}

	// An actual event proves the watch is established.
	createProbe("probe-before")
	expectProbe("probe-before")

	for _, path := range inactivePaths {
		// The watch on the logical cluster itself is terminated by the
		// same reconcile that used to terminate wildcard watches, so its
		// closure shows that the inactivation has been fully processed.
		clusterWatcher, err := kubeClient.Cluster(path).CoreV1().ConfigMaps("default").Watch(t.Context(), v1.ListOptions{})
		require.NoError(t, err)
		t.Cleanup(clusterWatcher.Stop)

		t.Logf("Mark logical cluster %s inactive", path)
		setInactiveAnnotation(t, kcpClient, path, true)
		drainAndExpectClose(t, clusterWatcher)
	}

	t.Log("Verify that the wildcard watch is still open and delivers events from active logical clusters")
	createProbe("probe-after")
	expectProbe("probe-after")
}
