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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
)

func newTestController() *Controller {
	return &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "test"},
		),
		shardWorkspaceInformers:      map[string]cache.SharedIndexInformer{},
		shardLogicalClusterInformers: map[string]cache.SharedIndexInformer{},
		shardWorkspaceStopCh:         map[string]chan struct{}{},
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
	if !found || result.URL != "https://old/clusters/myws" {
		t.Fatalf("precondition failed: found=%v url=%q", found, result.URL)
	}

	c.onShardUpdate(ctx, shardObj("alpha", "https://old"), shardObj("alpha", "https://new"))
	// the per-shard informers repopulate the logical clusters after the
	// restart triggered by the update; simulate that.
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	result, found = c.state.LookupURL(logicalcluster.NewPath("myws"))
	if !found {
		t.Fatal("shard base URL was lost after a baseURL change; workspaces on the shard are unroutable")
	}
	if result.URL != "https://new/clusters/myws" {
		t.Errorf("expected the new base URL to be used, got %q", result.URL)
	}
}

func TestOnShardUpdateSameBaseURLIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newTestController()

	c.onShardAdd(ctx, shardObj("alpha", "https://url"))
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	c.onShardUpdate(ctx, shardObj("alpha", "https://url"), shardObj("alpha", "https://url"))

	result, found := c.state.LookupURL(logicalcluster.NewPath("myws"))
	if !found || result.URL != "https://url/clusters/myws" {
		t.Errorf("expected state to be untouched, found=%v url=%q", found, result.URL)
	}
}

func TestOnShardDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newTestController()

	c.onShardAdd(ctx, shardObj("alpha", "https://url"))
	c.state.UpsertLogicalCluster("alpha", logicalClusterObj("myws"))

	c.onShardDelete(shardObj("alpha", "https://url"))

	if _, found := c.state.LookupURL(logicalcluster.NewPath("myws")); found {
		t.Error("expected the shard's clusters to be unroutable after delete")
	}
}
