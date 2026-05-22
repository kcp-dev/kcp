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
	framework.Suite(t, "control-plane")

	// The test requires a separate server as marking a cluster inactive
	// breaks wildcard watches which may affect other tests.
	server := kcptesting.PrivateKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kcpClient, err := kcpclientset.NewForConfig(cfg)
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
	framework.Suite(t, "control-plane")

	// The test requires a separate server as marking a cluster inactive
	// breaks wildcard watches which may affect other tests.
	server := kcptesting.PrivateKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kcpClient, err := kcpclientset.NewForConfig(cfg)
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

// TestInactiveLogicalClusterTerminatesWildcardWatch does the same check
// as TestInactiveLogicalClusterTerminatesClusterScopedWatch but for
// a wildcard watch.
func TestInactiveLogicalClusterTerminatesWildcardWatch(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// The test requires a separate server as marking a cluster inactive
	// breaks wildcard watches which may affect other tests.
	server := kcptesting.PrivateKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kcpClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	wildcardCfg := server.RootShardSystemMasterBaseConfig(t)
	wildcardKubeClient, err := kcpkubernetesclientset.NewForConfig(wildcardCfg)
	require.NoError(t, err)

	t.Log("Opening wildcard watch on ConfigMaps before marking inactive")
	watcher, err := wildcardKubeClient.CoreV1().ConfigMaps().Watch(t.Context(), v1.ListOptions{})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)

	t.Log("Mark logical cluster inactive")
	setInactiveAnnotation(t, kcpClient, orgPath, true)

	t.Log("Verify that the open wildcard watch is terminated")
	drainAndExpectClose(t, watcher)

	t.Log("Remove inactive annotation again to leave the shared server clean")
	setInactiveAnnotation(t, kcpClient, orgPath, false)
}
