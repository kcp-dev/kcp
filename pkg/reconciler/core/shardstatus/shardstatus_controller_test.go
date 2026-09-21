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

package shardstatus

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestProcessPublishesUsedWorkspaces(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		name           string
		initialUsed    corev1.ResourceList
		count          int64
		expectedUsed   string
		expectedUpdate bool
	}{
		{
			name:           "used is set from scratch",
			count:          42,
			expectedUsed:   "42",
			expectedUpdate: true,
		},
		{
			name: "used is updated when the count changed",
			initialUsed: corev1.ResourceList{
				corev1alpha1.ResourceWorkspaces: resource.MustParse("41"),
			},
			count:          43,
			expectedUsed:   "43",
			expectedUpdate: true,
		},
		{
			name: "no update when the count is unchanged",
			initialUsed: corev1.ResourceList{
				corev1alpha1.ResourceWorkspaces: resource.MustParse("42"),
			},
			count:          42,
			expectedUpdate: false,
		},
		{
			name:           "zero count is published",
			count:          0,
			expectedUsed:   "0",
			expectedUpdate: true,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			shard := &corev1alpha1.Shard{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
				Status:     corev1alpha1.ShardStatus{Used: scenario.initialUsed},
			}
			var updated *corev1alpha1.Shard
			c := &Controller{
				queue: workqueue.NewTypedRateLimitingQueueWithConfig(
					workqueue.DefaultTypedControllerRateLimiter[string](),
					workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
				),
				shardName: "alpha",
				getShard: func(ctx context.Context) (*corev1alpha1.Shard, error) {
					return shard, nil
				},
				updateShardStatus: func(ctx context.Context, shard *corev1alpha1.Shard) error {
					updated = shard
					return nil
				},
				countLogicalClusters: func() int64 {
					return scenario.count
				},
			}
			if err := c.process(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !scenario.expectedUpdate {
				if updated != nil {
					t.Fatalf("expected no status update, got %v", updated.Status.Used)
				}
				return
			}
			if updated == nil {
				t.Fatal("expected a status update")
			}
			used := updated.Status.Used[corev1alpha1.ResourceWorkspaces]
			if used.String() != scenario.expectedUsed {
				t.Errorf("expected status.used.workspaces to be %s, got %s", scenario.expectedUsed, used.String())
			}
		})
	}
}
