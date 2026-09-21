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

// Package shard covers the operational surface of a Shard - cordoning and the
// workspace scheduling limits - end to end: an admin writes the intent through
// the Admin workspace, the owning shard acknowledges it in status, and the
// workspace scheduler acts on it.
//
// These tests exercise the full chain that unit tests have to fake:
//
//	admin -> Admin workspace -> cache copy -> replication -> authoritative Shard
//	      -> shard controllers (ack, self-reported status.used)
//	      -> replication -> cache -> workspace scheduler
//
// Each test cordons or fills the only shard of its server, which stops every
// workspace from being scheduled, so they all run against their own
// PrivateKcpServer rather than the shared one.
package shard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	utilconditions "github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestShardCordoning cordons the only shard through the Admin workspace and
// checks both halves of the contract: the shard acknowledges the cordon with
// Schedulable=False, and the scheduler stops placing workspaces on it. Then it
// uncordons and checks that both recover.
func TestShardCordoning(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// cordoning the only shard stops all scheduling.
	server := kcptesting.PrivateKcpServer(t)
	ctx, orgClient, adminClient := setup(t, server)

	t.Logf("Cordon the shard through the Admin workspace")
	updateShard(ctx, t, adminClient, func(shard *corev1alpha1.Shard) {
		if shard.Annotations == nil {
			shard.Annotations = map[string]string{}
		}
		shard.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey] = "true"
	})

	t.Logf("The shard acknowledges the cordon with Schedulable=False")
	kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
		return adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
	}, kcptestinghelpers.IsNot(corev1alpha1.ShardSchedulable).WithReason(corev1alpha1.ShardReasonCordoned))

	workspaceName := createUntilUnschedulable(ctx, t, server, orgClient)

	t.Logf("Uncordon the shard through the Admin workspace")
	updateShard(ctx, t, adminClient, func(shard *corev1alpha1.Shard) {
		delete(shard.Annotations, corev1alpha1.ShardUnschedulableAnnotationKey)
	})

	t.Logf("The shard acknowledges the uncordon with Schedulable=True")
	kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
		return adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
	}, kcptestinghelpers.Is(corev1alpha1.ShardSchedulable))

	t.Logf("The waiting workspace is scheduled once the shard accepts workspaces again")
	kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
		return orgClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(tenancyv1alpha1.WorkspaceScheduled))

	t.Logf("The scheduled workspace points at the shard that accepted it")
	workspace, err := orgClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	require.NoError(t, err)
	orgLogicalCluster, err := orgClient.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	require.NoError(t, err)
	shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
	require.NoError(t, err)
	path := fmt.Sprintf("%s:%s", orgLogicalCluster.Annotations[core.LogicalClusterPathAnnotationKey], workspace.Name)
	require.Equal(t, shard.Spec.BaseURL+"/clusters/"+path, workspace.Spec.URL, "incorrect workspace URL")
}

// TestShardHardWorkspaceLimit is the test that ties the whole feature together:
// it pins the hard limit to the workspace count the shard reports for itself,
// so the shard refuses new workspaces, and checks that the scheduler honours it.
//
// Reaching the limit at all depends on status.used being written to the
// authoritative Shard object and replicated to the cache the scheduler reads
// from. Unit tests hand the scheduler a Shard with status.used already filled
// in, so only this test covers that path.
func TestShardHardWorkspaceLimit(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// filling the only shard stops all scheduling.
	server := kcptesting.PrivateKcpServer(t)
	ctx, orgClient, adminClient := setup(t, server)

	t.Logf("Read the workspace count the shard reports for itself")
	var used int64
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		quantity, ok := shard.Status.Used[corev1alpha1.ResourceWorkspaces]
		require.True(c, ok, "shard does not report status.used[workspaces] yet")
		used = quantity.Value()
		// a hard limit of 0 is disabled by design, so the test needs the shard
		// to hold at least one logical cluster before it can pin a limit to it.
		require.Positive(c, used, "shard reports no workspaces yet")
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Shard reports %d workspaces", used)

	t.Logf("Pin the hard limit to that count, so the shard is at its limit")
	updateShard(ctx, t, adminClient, func(shard *corev1alpha1.Shard) {
		shard.Spec.ResourceLimits = &corev1alpha1.ShardResourceLimits{
			Hard: corev1.ResourceList{corev1alpha1.ResourceWorkspaces: *resource.NewQuantity(used, resource.DecimalSI)},
		}
	})

	t.Logf("The shard acknowledges the limit it now enforces")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		cond := utilconditions.Get(shard, corev1alpha1.ShardResourceLimitsApplied)
		require.NotNil(c, cond, "expected the ResourceLimitsApplied condition")
		require.Equal(c, corev1.ConditionTrue, cond.Status, "unexpected condition: %v", cond)
		require.Equal(c, fmt.Sprintf("soft/hard: workspaces=-/%d", used), cond.Message)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	workspaceName := createUntilUnschedulable(ctx, t, server, orgClient)

	t.Logf("Lift the limit through the Admin workspace")
	updateShard(ctx, t, adminClient, func(shard *corev1alpha1.Shard) {
		shard.Spec.ResourceLimits = nil
	})

	t.Logf("The shard acknowledges that no limits are configured")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		cond := utilconditions.Get(shard, corev1alpha1.ShardResourceLimitsApplied)
		require.NotNil(c, cond, "expected the ResourceLimitsApplied condition")
		require.Equal(c, "no limits configured", cond.Message)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("The waiting workspace is scheduled once there is capacity again")
	kcptestinghelpers.EventuallyCondition(t, func() (utilconditions.Getter, error) {
		return orgClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(tenancyv1alpha1.WorkspaceScheduled))
}

// TestShardReportsUsedWorkspaces checks that a shard's self-reported workspace
// count reaches the Admin workspace and grows as workspaces are added. The
// count is produced from each shard's local LogicalCluster informer, debounced,
// written to the shard's own object and replicated, so nothing short of an e2e
// covers it.
func TestShardReportsUsedWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// this test does not break scheduling, but it asserts on a count that
	// every other test would perturb.
	server := kcptesting.PrivateKcpServer(t)
	ctx, _, adminClient := setup(t, server)

	usedWorkspaces := func(c require.TestingT) int64 {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		quantity, ok := shard.Status.Used[corev1alpha1.ResourceWorkspaces]
		require.True(c, ok, "shard does not report status.used[workspaces]")
		return quantity.Value()
	}

	var before int64
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		before = usedWorkspaces(c)
		require.Positive(c, before, "shard reports no workspaces yet")
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Shard reports %d workspaces before creating another one", before)

	kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	t.Logf("The shard reports the added workspace through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.Greater(c, usedWorkspaces(c), before, "shard did not report the added workspace")
	}, wait.ForeverTestTimeout, time.Second)
}

// setup starts the clients every test here needs: one for an organization
// workspace to create workspaces in, and one for the Admin workspace, which is
// the only surface that accepts operational writes to a Shard.
func setup(t *testing.T, server kcptestingserver.RunningServer) (context.Context, kcpclientset.Interface, kcpclientset.Interface) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	// the organization workspace has to be created while the shard still
	// accepts workspaces, so this runs before any test cordons or fills it.
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	clusterClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	adminClient, err := kcptesting.AdminWorkspaceClient(cfg)
	require.NoError(t, err, "failed to construct Admin workspace client")

	return ctx, clusterClient.Cluster(orgPath), adminClient
}

// updateShard applies mutate to the shard through the Admin workspace, retrying
// on conflict. Only the cordon annotation and spec.resourceLimits may be
// changed this way; everything else on a Shard is owned by the shard itself.
func updateShard(ctx context.Context, t *testing.T, adminClient kcpclientset.Interface, mutate func(*corev1alpha1.Shard)) {
	t.Helper()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		mutate(shard)
		_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

// createUntilUnschedulable keeps creating workspaces until one comes out
// unschedulable, and returns its name.
//
// A workspace cannot become unschedulable after the fact: once it is placed on
// a shard it stays there, because workspaces are never moved. Whatever makes
// the shard refuse work - a cordon or a hard limit - is written to the cache
// copy and takes a moment to reach the scheduler's informer, so workspaces
// created in that window still get scheduled. Retrying until one is refused is
// what makes this deterministic.
func createUntilUnschedulable(ctx context.Context, t *testing.T, server kcptestingserver.RunningServer, orgClient kcpclientset.Interface) string {
	t.Helper()

	t.Logf("Create workspaces until one is refused")
	var workspaceName string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ws, err := orgClient.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "limited-"},
		}, metav1.CreateOptions{})
		require.NoError(c, err)
		workspaceName = ws.Name

		require.NoError(c, wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(ctx context.Context) (bool, error) {
			ws, err = orgClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return utilconditions.Get(ws, tenancyv1alpha1.WorkspaceScheduled) != nil, nil
		}))

		cond := utilconditions.Get(ws, tenancyv1alpha1.WorkspaceScheduled)
		require.Equalf(c, tenancyv1alpha1.WorkspaceReasonUnschedulable, cond.Reason, "workspace %s was not refused", ws.Name)
	}, wait.ForeverTestTimeout, time.Second)

	server.Artifact(t, func() (runtime.Object, error) {
		return orgClient.TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	})

	return workspaceName
}
