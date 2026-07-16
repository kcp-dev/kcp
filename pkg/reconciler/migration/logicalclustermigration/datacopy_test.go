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
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
)

// newTestController returns a Controller with just enough wired up to
// exercise acquireOriginClient/releaseOriginClient: a shardLister backed by
// the given fixture shards (all in the root cluster, matching how
// c.shardLister.Cluster(core.RootCluster).Get(name) is called), and a
// harmless rest.Config. No network calls are made by these tests -
// kcpclientset.NewForConfig only builds a client struct, it doesn't dial.
func newTestController(t *testing.T, shards ...*corev1alpha1.Shard) *Controller {
	t.Helper()

	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{})
	for _, shard := range shards {
		require.NoError(t, indexer.Add(shard))
	}

	return &Controller{
		shardLister:                       corev1alpha1listers.NewShardClusterLister(indexer),
		externalLogicalClusterAdminConfig: &rest.Config{Host: "https://unused.invalid"},
		originClients:                     make(map[string]*originClientEntry),
	}
}

func newTestShard(name, virtualWorkspaceURL string) *corev1alpha1.Shard {
	return &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
		},
		Spec: corev1alpha1.ShardSpec{
			VirtualWorkspaceURL: virtualWorkspaceURL,
		},
	}
}

func TestAcquireOriginClient_sharesOneClientAcrossLogicalClustersOnSameShard(t *testing.T) {
	t.Parallel()

	c := newTestController(t, newTestShard("shard-a", "https://shard-a.invalid"))

	client1, err := c.acquireOriginClient(logicalcluster.Name("lc-1"), "shard-a")
	require.NoError(t, err)

	client2, err := c.acquireOriginClient(logicalcluster.Name("lc-2"), "shard-a")
	require.NoError(t, err)

	require.Same(t, client1, client2, "two logical clusters migrating from the same origin shard must share one client")
}

func TestAcquireOriginClient_separateShardsGetSeparateClients(t *testing.T) {
	t.Parallel()

	c := newTestController(t,
		newTestShard("shard-a", "https://shard-a.invalid"),
		newTestShard("shard-b", "https://shard-b.invalid"),
	)

	clientA, err := c.acquireOriginClient(logicalcluster.Name("lc-1"), "shard-a")
	require.NoError(t, err)

	clientB, err := c.acquireOriginClient(logicalcluster.Name("lc-1"), "shard-b")
	require.NoError(t, err)

	require.NotSame(t, clientA, clientB, "different origin shards must not share a client")
}

func TestAcquireOriginClient_repeatedAcquireForSameLogicalClusterIsIdempotent(t *testing.T) {
	t.Parallel()

	c := newTestController(t, newTestShard("shard-a", "https://shard-a.invalid"))
	lc := logicalcluster.Name("lc-1")

	client1, err := c.acquireOriginClient(lc, "shard-a")
	require.NoError(t, err)

	// Simulate copying several pages of the same migration: acquiring
	// again for the same (shard, lc) pair must not create additional refs.
	for range 3 {
		client, err := c.acquireOriginClient(lc, "shard-a")
		require.NoError(t, err)
		require.Same(t, client1, client)
	}

	c.originClientsMu.Lock()
	refCount := len(c.originClients["shard-a"].refs)
	c.originClientsMu.Unlock()
	require.Equal(t, 1, refCount, "repeated acquires for the same logical cluster must not inflate the refcount")

	// A single release must be enough to fully drop it.
	c.releaseOriginClient(lc, "shard-a")
	c.originClientsMu.Lock()
	_, stillCached := c.originClients["shard-a"]
	c.originClientsMu.Unlock()
	require.False(t, stillCached, "one release should fully drop an entry that was only ever acquired by one logical cluster")
}

func TestReleaseOriginClient_keepsSharedClientAliveUntilLastUserReleases(t *testing.T) {
	t.Parallel()

	c := newTestController(t, newTestShard("shard-a", "https://shard-a.invalid"))

	client1, err := c.acquireOriginClient(logicalcluster.Name("lc-1"), "shard-a")
	require.NoError(t, err)
	_, err = c.acquireOriginClient(logicalcluster.Name("lc-2"), "shard-a")
	require.NoError(t, err)

	// lc-1 finishes first: the shared client must survive because lc-2 is
	// still using it.
	c.releaseOriginClient(logicalcluster.Name("lc-1"), "shard-a")

	c.originClientsMu.Lock()
	entry, stillCached := c.originClients["shard-a"]
	c.originClientsMu.Unlock()
	require.True(t, stillCached, "client must stay cached while lc-2 is still using it")
	require.Same(t, client1, entry.client)

	// lc-2 finishes: now it's the last user, the entry must be dropped.
	c.releaseOriginClient(logicalcluster.Name("lc-2"), "shard-a")

	c.originClientsMu.Lock()
	_, stillCached = c.originClients["shard-a"]
	c.originClientsMu.Unlock()
	require.False(t, stillCached, "client must be dropped once the last user releases it")
}

func TestReleaseOriginClient_neverAcquiredIsANoop(t *testing.T) {
	t.Parallel()

	c := newTestController(t, newTestShard("shard-a", "https://shard-a.invalid"))

	require.NotPanics(t, func() {
		c.releaseOriginClient(logicalcluster.Name("never-acquired"), "shard-a")
	})
	require.NotPanics(t, func() {
		c.releaseOriginClient(logicalcluster.Name("lc-1"), "shard-that-does-not-exist")
	})
}
