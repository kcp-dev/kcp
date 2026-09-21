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

package admin

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAdminWorkspaceShards exercises the Admin workspace (/services/admin):
// the aggregated shards view, the operational writes it accepts - cordoning
// and scheduling limits, each written to the cache copy and applied to the
// authoritative object by the hosting shard - the allow-list enforcement, and
// the admission protection of direct edits.
func TestAdminWorkspaceShards(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// cordoning the only shard is destructive.
	server := kcptesting.PrivateKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err)
	rootClient := kcpClusterClient.Cluster(core.RootCluster.Path())

	// the authoritative Shard object lives in the shard-local system:shard
	// logical cluster; reading it requires a privileged client.
	systemClusterClient, err := kcpclusterclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err)
	authoritativeClient := systemClusterClient.Cluster(configshard.SystemShardCluster.Path())

	vwCfg := rest.CopyConfig(cfg)
	vwURL, err := url.Parse(cfg.Host)
	require.NoError(t, err)
	vwURL.Path = "/services/admin"
	vwCfg.Host = vwURL.String()
	adminClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err)

	t.Logf("List shards through the Admin workspace at %s", vwCfg.Host)
	var shards *corev1alpha1.ShardList
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shards, err = adminClient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, shards.Items, "expected at least the root shard in the admin view")
		for _, shard := range shards.Items {
			require.NotContains(c, shard.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Get every listed shard by name through the Admin workspace")
	for _, listed := range shards.Items {
		for _, rv := range []string{"", "0", shards.ResourceVersion} {
			shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, listed.Name, metav1.GetOptions{ResourceVersion: rv})
			require.NoError(t, err, "failed to get shard %q with resourceVersion %q", listed.Name, rv)
			require.Equal(t, listed.Name, shard.Name)
			require.NotContains(t, shard.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
		}
	}

	_, err = adminClient.CoreV1alpha1().Shards().Get(ctx, "does-not-exist", metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err), "expected NotFound for an unknown shard, got %v", err)

	t.Logf("Read-only Shard representations are mirrored into the root workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		representations, err := rootClient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, representations.Items, "expected the root shard's representation in the root workspace")
		for _, shard := range representations.Items {
			require.NotContains(c, shard.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
			require.NotEmpty(c, shard.Spec.BaseURL)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Changing the spec through the Admin workspace is forbidden")
	shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
	require.NoError(t, err)
	tampered := shard.DeepCopy()
	tampered.Spec.ExternalURL = "https://tampered.kcp.test.dev"
	_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, tampered, metav1.UpdateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)

	t.Logf("Direct creation of Shard objects in the root workspace is forbidden")
	_, err = rootClient.CoreV1alpha1().Shards().Create(ctx, &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{Name: "fake"},
		Spec:       corev1alpha1.ShardSpec{BaseURL: "https://fake.kcp.test.dev"},
	}, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)

	t.Logf("The Shard representations in the root workspace are read-only: update, patch and delete are forbidden")
	// the mirror controller may write to the representation concurrently, so
	// retry conflicts until admission's forbidden is observed.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		representation, err := rootClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		tamperedRepresentation := representation.DeepCopy()
		tamperedRepresentation.Spec.ExternalURL = "https://tampered.kcp.test.dev"
		_, err = rootClient.CoreV1alpha1().Shards().Update(ctx, tamperedRepresentation, metav1.UpdateOptions{})
		require.True(c, apierrors.IsForbidden(err), "expected update of the representation to be forbidden, got: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	_, err = rootClient.CoreV1alpha1().Shards().Patch(ctx, corev1alpha1.RootShard, types.MergePatchType, []byte(`{"metadata":{"annotations":{"tampered":"true"}}}`), metav1.PatchOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected patch of the representation to be forbidden, got: %v", err)
	err = rootClient.CoreV1alpha1().Shards().Delete(ctx, corev1alpha1.RootShard, metav1.DeleteOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected delete of the representation to be forbidden, got: %v", err)

	t.Logf("Cordon the shard through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		if shard.Annotations == nil {
			shard.Annotations = map[string]string{}
		}
		shard.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey] = "true"
		_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for the cordon to be applied to the authoritative Shard object")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		authoritative, err := authoritativeClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, "true", authoritative.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey])
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Uncordon the shard through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		delete(shard.Annotations, corev1alpha1.ShardUnschedulableAnnotationKey)
		_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for the uncordon to be applied to the authoritative Shard object")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		authoritative, err := authoritativeClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		require.NotContains(c, authoritative.Annotations, corev1alpha1.ShardUnschedulableAnnotationKey)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Set workspace scheduling limits through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		shard.Spec.ResourceLimits = &corev1alpha1.ShardResourceLimits{
			Soft: corev1.ResourceList{corev1alpha1.ResourceWorkspaces: resource.MustParse("450")},
			Hard: corev1.ResourceList{corev1alpha1.ResourceWorkspaces: resource.MustParse("500")},
		}
		_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for the limits to be applied to the authoritative Shard object")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		authoritative, err := authoritativeClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		require.NotNil(c, authoritative.Spec.ResourceLimits)
		soft := authoritative.Spec.ResourceLimits.Soft[corev1alpha1.ResourceWorkspaces]
		hard := authoritative.Spec.ResourceLimits.Hard[corev1alpha1.ResourceWorkspaces]
		require.Equal(c, int64(450), soft.Value())
		require.Equal(c, int64(500), hard.Value())
		// the shard keeps owning the rest of its object.
		require.NotEmpty(c, authoritative.Spec.BaseURL)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("The shard acknowledges the limits back through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		// conditions are shard-owned and travel shard -> cache, so the message
		// naming the limits in force proves the whole round trip completed.
		cond := conditions.Get(shard, corev1alpha1.ShardResourceLimitsApplied)
		require.NotNil(c, cond, "expected the ResourceLimitsApplied condition")
		require.Equal(c, corev1.ConditionTrue, cond.Status, "unexpected condition: %v", cond)
		require.Equal(c, "soft/hard: workspaces=450/500", cond.Message)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Clear the limits through the Admin workspace")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shard, err := adminClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		shard.Spec.ResourceLimits = nil
		_, err = adminClient.CoreV1alpha1().Shards().Update(ctx, shard, metav1.UpdateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for the limits to be cleared on the authoritative Shard object and acknowledged")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		authoritative, err := authoritativeClient.CoreV1alpha1().Shards().Get(ctx, corev1alpha1.RootShard, metav1.GetOptions{})
		require.NoError(c, err)
		require.Nil(c, authoritative.Spec.ResourceLimits)
		cond := conditions.Get(authoritative, corev1alpha1.ShardResourceLimitsApplied)
		require.NotNil(c, cond, "expected the ResourceLimitsApplied condition")
		require.Equal(c, "no limits configured", cond.Message, "a stale message would claim limits are still in force")
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
