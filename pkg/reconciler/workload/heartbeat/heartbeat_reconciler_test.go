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

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestManager(t *testing.T) {
	for _, c := range []struct {
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
		t.Run(c.desc, func(t *testing.T) {
			var enqueued time.Duration
			enqueueFunc := func(_ *workloadv1alpha1.SyncTarget, dur time.Duration) {
				enqueued = dur
			}
			mgr := clusterManager{
				heartbeatThreshold:  time.Minute,
				enqueueClusterAfter: enqueueFunc,
			}
			ctx := context.Background()
			heartbeat := metav1.NewTime(c.lastHeartbeatTime)
			cl := &workloadv1alpha1.SyncTarget{
				Status: workloadv1alpha1.SyncTargetStatus{
					Conditions: []conditionsv1alpha1.Condition{{
						Type:   workloadv1alpha1.HeartbeatHealthy,
						Status: corev1.ConditionTrue,
					}},
					LastSyncerHeartbeatTime: &heartbeat,
				},
			}
			if err := mgr.Reconcile(ctx, cl); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			// actual enqueued time must not be more than 30s off from desired enqueue time.
			delta := 30 * time.Millisecond
			if c.wantDur-delta > enqueued {
				t.Errorf("next enqueue time; got %s, want %s", enqueued, c.wantDur)
			}
			isReady := cl.GetConditions()[0].Status == corev1.ConditionTrue
			if isReady != c.wantReady {
				t.Errorf("cluster Ready; got %t, want %t", isReady, c.wantReady)
			}
			// TODO: check wantReady.
		})
	}
}
