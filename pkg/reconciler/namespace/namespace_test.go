/*
Copyright 2022 The KCP Authors.

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

package namespace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestEnqueueStrategyForCluster(t *testing.T) {
	previousTime := time.Now().Add(-1 * time.Hour)
	futureTime := time.Now().Add(1 * time.Hour)

	testCases := map[string]struct {
		ready         bool
		unschedulable bool
		evictAfter    *time.Time
		strategy      clusterEnqueueStrategy
		pendingCordon bool
	}{
		// Existing assignments need to be reassigned
		"not ready -> enqueue scheduled": {
			strategy: enqueueScheduled,
		},
		// Existing assignments need to be reassigned
		"not ready, cordoned -> enqueue scheduled": {
			evictAfter: &previousTime,
			strategy:   enqueueScheduled,
		},
		// Existing assignments need to be reassigned
		"ready, cordoned -> enqueue scheduled": {
			ready:      true,
			evictAfter: &previousTime,
			strategy:   enqueueScheduled,
		},
		// Existing assignments are maintained, no new assignments possible
		"ready, unschedulable -> enqueue nothing": {
			ready:         true,
			unschedulable: true,
			strategy:      enqueueNothing,
		},
		// Existing assignments are maintained, no new assignments possible
		"ready, unschedulable, future cordon -> enqueue nothing + pending cordon": {
			ready:         true,
			unschedulable: true,
			evictAfter:    &futureTime,
			strategy:      enqueueNothing,
			pendingCordon: true,
		},
		// New assignments are possible
		"ready  -> enqueue unscheduled": {
			ready:    true,
			strategy: enqueueUnscheduled,
		},
		// New assignments are possible
		"ready, future cordon -> enqueue unscheduled + pending cordon": {
			ready:         true,
			evictAfter:    &futureTime,
			strategy:      enqueueUnscheduled,
			pendingCordon: true,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			cluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: clusterv1alpha1.ClusterSpec{
					Unschedulable: testCase.unschedulable,
				},
			}
			if testCase.ready {
				conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
			}
			if testCase.evictAfter != nil {
				evictAfter := metav1.NewTime(*testCase.evictAfter)
				cluster.Spec.EvictAfter = &evictAfter
			}
			strategy, pendingCordon := enqueueStrategyForCluster(cluster)
			require.Equal(t, testCase.strategy, strategy, "unexpected strategy")
			require.Equal(t, testCase.pendingCordon, pendingCordon, "unexpected pendingCordon")
		})
	}

}
