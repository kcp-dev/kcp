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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/integration/framework"
)

// TestPerClusterContextCancelsWatches verifies that active watches get
// cancelled when the context of the respective logical cluster is
// cancelled through the context manager.
func TestPerClusterContextCancelsWatches(t *testing.T) {
	t.Parallel()

	server, kcpClientSet, kubeClient := framework.StartTestServer(t)

	workspace := kcptesting.NewLowLevelWorkspaceFixture(t, kcpClientSet, kcpClientSet, core.RootCluster.Path(), kcptesting.WithNamePrefix("ctx-cancel"))
	lcName := logicalcluster.Name(workspace.Spec.Cluster)

	t.Logf("Opening watch on ConfigMaps in workspace %s", lcName)
	watcher, err := kubeClient.Cluster(lcName.Path()).CoreV1().ConfigMaps("default").Watch(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	t.Cleanup(watcher.Stop)

	t.Logf("Verifying watch is alive by creating a ConfigMap and receiving an event")
	_, err = kubeClient.Cluster(lcName.Path()).CoreV1().ConfigMaps("default").Create(
		t.Context(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "trigger-cm"},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	select {
	case event, ok := <-watcher.ResultChan():
		// TODO(ntnn): Verify the event matches the created configmap
		require.True(t, ok, "watch channel closed unexpectedly before cancel")
		require.NotEqual(t, watch.Error, event.Type, "unexpected error event before cancel")
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("did not receive watch event - watch is not working")
	}

	t.Logf("Cancelling per-cluster context for %s", lcName)
	server.Server.ClusterContextManager.Delete(lcName.Path(), errors.New("cancelling logical cluster connections"))

	t.Logf("Verifying watch gets terminated")
	select {
	case _, ok := <-watcher.ResultChan():
		if ok {
			// Might get an error event or the channel might deliver remaining buffered events.
			// Drain until closed.
			for range watcher.ResultChan() {
			}
		}
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("watch was not terminated after context cancellation")
	}

	t.Logf("Verifying a new watch still works after cancellation")
	newWatcher, err := kubeClient.Cluster(lcName.Path()).CoreV1().ConfigMaps("default").Watch(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	t.Cleanup(newWatcher.Stop)

	_, err = kubeClient.Cluster(lcName.Path()).CoreV1().ConfigMaps("default").Create(
		t.Context(),
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "post-cancel-cm"},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	select {
	case event, ok := <-newWatcher.ResultChan():
		// TODO(ntnn): Verify the event matches the created configmap
		require.True(t, ok, "new watch channel closed unexpectedly")
		require.NotEqual(t, watch.Error, event.Type, "unexpected error event on new watch")
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("new watch did not receive event - new watches are broken after cancel")
	}
}
