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

package index

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
)

func newTestController() *Controller {
	shardIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	return &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "test"},
		),
		shardIndexer:                 shardIndexer,
		shardLister:                  corev1alpha1listers.NewShardLister(shardIndexer),
		shardWorkspaceInformers:      map[string]cache.SharedIndexInformer{},
		shardLogicalClusterInformers: map[string]cache.SharedIndexInformer{},
		shardWorkspaceStopCh:         map[string]chan struct{}{},
		shardInformersSynced:         map[string][]cache.InformerSynced{},
		state:                        *index.New(nil),
	}
}

func shardObj(name, baseURL string) *corev1alpha1.Shard {
	return &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1alpha1.ShardSpec{BaseURL: baseURL},
	}
}

func logicalClusterObj(clusterName string) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName},
		},
	}
}

func TestOnShardUpdateBaseURLChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newTestController()

	c.onShardAdd(ctx, shardObj("alpha", "https://old"))
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	result, found := c.state.LookupURL(logicalcluster.NewPath("myws"))
	require.True(t, found, "precondition failed")
	require.Equal(t, "https://old/clusters/myws", result.URL, "precondition failed")

	c.onShardUpdate(ctx, shardObj("alpha", "https://old"), shardObj("alpha", "https://new"))
	// the per-shard informers repopulate the logical clusters after the
	// restart triggered by the update; simulate that.
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	result, found = c.state.LookupURL(logicalcluster.NewPath("myws"))
	require.True(t, found, "shard base URL was lost after a baseURL change; workspaces on the shard are unroutable")
	require.Equal(t, "https://new/clusters/myws", result.URL, "expected the new base URL to be used")
}

func TestOnShardUpdateSameBaseURLIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newTestController()

	c.onShardAdd(ctx, shardObj("alpha", "https://url"))
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	c.onShardUpdate(ctx, shardObj("alpha", "https://url"), shardObj("alpha", "https://url"))

	result, found := c.state.LookupURL(logicalcluster.NewPath("myws"))
	require.True(t, found, "expected state to be untouched")
	require.Equal(t, "https://url/clusters/myws", result.URL, "expected state to be untouched")
}

func TestOnShardDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newTestController()

	c.onShardAdd(ctx, shardObj("alpha", "https://url"))
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	c.onShardDelete(shardObj("alpha", "https://url"))

	_, found := c.state.LookupURL(logicalcluster.NewPath("myws"))
	require.False(t, found, "expected the shard's clusters to be unroutable after delete")
}

// TestReady walks the readiness of the index through the life of a shard:
// unknown, known but informers not started, started but still syncing,
// synced, and gone.
func TestReady(t *testing.T) {
	t.Parallel()
	c := newTestController()

	require.NoError(t, c.Ready(), "an index without shards is ready")

	alpha := shardObj("alpha", "https://alpha")
	require.NoError(t, c.shardIndexer.Add(alpha))
	err := c.Ready()
	require.Error(t, err, "a shard without informers keeps the index not ready")
	require.Contains(t, err.Error(), `"alpha"`)
	require.Contains(t, err.Error(), "not started")

	synced := false
	c.lock.Lock()
	c.shardInformersSynced["alpha"] = []cache.InformerSynced{func() bool { return true }, func() bool { return synced }}
	c.lock.Unlock()
	err = c.Ready()
	require.Error(t, err, "a shard whose informers have not synced keeps the index not ready")
	require.Contains(t, err.Error(), "not synced")

	synced = true
	require.NoError(t, c.Ready(), "all informers synced")

	beta := shardObj("beta", "https://beta")
	require.NoError(t, c.shardIndexer.Add(beta))
	require.Error(t, c.Ready(), "a new shard is not ready until its informers are")

	c.stopShard("beta")
	require.NoError(t, c.shardIndexer.Delete(beta))
	require.NoError(t, c.Ready(), "a deleted shard no longer counts")

	c.stopShard("alpha")
	require.Error(t, c.Ready(), "stopping the informers of a shard that still exists makes the index not ready")
}

// TestReadyCordonedShard checks that a cordoned shard, i.e. one in
// maintenance, does not hold the index back, and counts again once it is
// uncordoned.
func TestReadyCordonedShard(t *testing.T) {
	t.Parallel()
	c := newTestController()

	alpha := shardObj("alpha", "https://alpha")
	require.NoError(t, c.shardIndexer.Add(alpha))
	c.lock.Lock()
	c.shardInformersSynced["alpha"] = []cache.InformerSynced{func() bool { return true }}
	c.lock.Unlock()

	beta := shardObj("beta", "https://beta")
	require.NoError(t, c.shardIndexer.Add(beta))
	require.Error(t, c.Ready(), "an uncordoned shard that is not indexed keeps the index not ready")

	cordoned := beta.DeepCopy()
	cordoned.Annotations = map[string]string{corev1alpha1.ShardUnschedulableAnnotationKey: "true"}
	require.NoError(t, c.shardIndexer.Update(cordoned))
	require.NoError(t, c.Ready(), "a cordoned shard does not count")

	synced := false
	c.lock.Lock()
	c.shardInformersSynced["beta"] = []cache.InformerSynced{func() bool { return synced }}
	c.lock.Unlock()
	require.NoError(t, c.shardIndexer.Update(beta))
	require.Error(t, c.Ready(), "an uncordoned shard counts again")

	synced = true
	require.NoError(t, c.Ready())
}
