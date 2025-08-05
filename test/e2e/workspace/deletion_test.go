/*
Copyright 2025 The KCP Authors.

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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	logicalclusterdeletion "github.com/kcp-dev/kcp/pkg/reconciler/core/logicalclusterdeletion/deletion"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestWorkspaceLogicalClusterRelationship tests that a workspace gets not
// deleted prematurely if its LogicalCluster has not yet been deleted.
func TestWorkspaceLogicalClusterRelationship(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	// This will create structure as below for testing:
	// └── root
	//   └── e2e-workspace-n784f
	//    └── test
	wsName := "test"
	fixtureRoot, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	testPath, _ := kcptesting.NewWorkspaceFixture(t, server, fixtureRoot, kcptesting.WithName(wsName))

	clientset, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client set")

	lc, err := clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, v1.GetOptions{})
	require.NoError(t, err, "error getting logicalcluster")

	// add a finalizer to the cluster, mimicking an external finalizer
	customFinalizer := "example.com/test"
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		lcsUpd := lc.DeepCopy()
		lcsUpd.Finalizers = append(lcsUpd.Finalizers, customFinalizer)
		_, err = clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Update(ctx, lcsUpd, v1.UpdateOptions{})
		require.NoError(c, err, "error updating logicalcluster")
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for finalizer to be applied and workspace to be deleted")

	// delete the workspace
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		err = clientset.Cluster(fixtureRoot).TenancyV1alpha1().Workspaces().Delete(ctx, wsName, v1.DeleteOptions{})
		require.NoError(c, err, "error deleting workspace")
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for finalizer to be applied and workspace to be deleted")

	// ensure that the deletion has propagated to the logicalcluster, meaning:
	// - it should have a deletion timestamp
	// - it should have the custom finalizer
	// - it should not have its own deletion finalizer anymore
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		lc, err := clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, v1.GetOptions{})
		require.NoError(c, err, "error getting logicalcluster")
		require.NotEqual(c, nil, lc.DeletionTimestamp)
		require.Contains(c, lc.Finalizers, customFinalizer)
		require.NotContains(c, lc.Finalizers, logicalclusterdeletion.LogicalClusterDeletionFinalizer)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for logicalcluster to be marked for deletion")

	// ensure that the workspace has a deletionTimestamp, but still has the
	// logicalcluster finalizer
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ws, err := clientset.Cluster(fixtureRoot).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		require.NoError(c, err, "error getting workspace")
		require.NotEqual(c, nil, ws.DeletionTimestamp)
		require.Contains(c, ws.Finalizers, corev1alpha1.LogicalClusterFinalizer)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace to be marked for deletion")

	// remove the custom finalizer from the logicalcluster object
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		lc, err := clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, v1.GetOptions{})
		require.NoError(c, err, "error getting logicalcluster")
		lcsUpd := lc.DeepCopy()
		lcsUpd.Finalizers = removeByValue(lcsUpd.Finalizers, customFinalizer)
		_, err = clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Update(ctx, lcsUpd, v1.UpdateOptions{})
		require.NoError(c, err, "error updating logicalcluster")
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "wainting for custom finalizer to be removed")

	// the logicalcluster should now be cleaned up successfully
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		_, err := clientset.Cluster(testPath).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, v1.GetOptions{})
		if apierrors.IsForbidden(err) {
			return true, ""
		}
		return false, "logicalcluster still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for logicalcluster to be deleted")

	// the workspace should now be cleaned up successfully
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		_, err := clientset.Cluster(fixtureRoot).TenancyV1alpha1().Workspaces().Get(ctx, wsName, v1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		return false, "workspace still exists"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace to be deleted")
}

func removeByValue[T comparable](l []T, item T) []T {
	for i, other := range l {
		if other == item {
			return append(l[:i], l[i+1:]...)
		}
	}
	return l
}
