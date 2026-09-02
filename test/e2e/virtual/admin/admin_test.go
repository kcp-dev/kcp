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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAdminWorkspaceShards exercises the Admin workspace (/services/admin):
// the aggregated shards view, cordoning through it (write to the cache copy,
// applied to the authoritative object by the hosting shard), the allow-list
// enforcement, and the admission protection of direct edits.
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
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		shards, err := adminClient.CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, shards.Items, "expected at least the root shard in the admin view")
		for _, shard := range shards.Items {
			require.NotContains(c, shard.Annotations, "kcp.io/shard", "cache bookkeeping annotations must be stripped")
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
}
