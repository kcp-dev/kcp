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

package heartbeat

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

type fakeDelayingQueue struct {
	workqueue.RateLimitingInterface
	duration time.Duration
}

var _ workqueue.DelayingInterface = (*fakeDelayingQueue)(nil)

func (f *fakeDelayingQueue) AddAfter(obj interface{}, duration time.Duration) {
	f.duration = duration
}

func TestReconcile(t *testing.T) {
	for _, tc := range []struct {
		desc              string
		lastHeartbeatTime time.Time
		wantDur           time.Duration
		wantReady         bool
	}{{
		desc:      "no last heartbeat",
		wantReady: false,
	}, {
		desc:              "recent enough heartbeat",
		lastHeartbeatTime: time.Now().Add(-10 * time.Second),
		wantDur:           50 * time.Second,
		wantReady:         true,
	}, {
		desc:              "not recent enough heartbeat",
		lastHeartbeatTime: time.Now().Add(-90 * time.Second),
		wantReady:         false,
	}} {
		t.Run(tc.desc, func(t *testing.T) {
			queue := &fakeDelayingQueue{
				RateLimitingInterface: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "testing"),
			}
			c := &Controller{
				queue:              queue,
				heartbeatThreshold: time.Minute,
			}
			ctx := context.Background()
			heartbeat := metav1.NewTime(tc.lastHeartbeatTime)
			syncTarget := &workloadv1alpha1.SyncTarget{
				Status: workloadv1alpha1.SyncTargetStatus{
					Conditions: []conditionsv1alpha1.Condition{{
						Type:   workloadv1alpha1.HeartbeatHealthy,
						Status: corev1.ConditionTrue,
					}},
					LastSyncerHeartbeatTime: &heartbeat,
				},
			}
			if err := c.reconcile(ctx, "somekey", syncTarget); err != nil {
				t.Fatalf("reconcile: %v", err)
			}

			// actual enqueued time must not be more than 30ms off from desired enqueue time.
			delta := 30 * time.Millisecond
			if tc.wantDur-delta > queue.duration {
				t.Errorf("next enqueue time; got %s, want %s", queue.duration, tc.wantDur)
			}
			isReady := syncTarget.GetConditions()[0].Status == corev1.ConditionTrue
			if isReady != tc.wantReady {
				t.Errorf("SyncTarget Ready; got %t, want %t", isReady, tc.wantReady)
			}
			// TODO: check wantReady.
		})
	}
}
