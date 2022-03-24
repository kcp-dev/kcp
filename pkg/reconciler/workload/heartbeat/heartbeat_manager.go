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
	"time"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/basecontroller"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

var _ basecontroller.ClusterReconcileImpl = (*clusterManager)(nil)

type clusterManager struct {
	heartbeatThreshold  time.Duration
	enqueueClusterAfter func(*workloadv1alpha1.WorkloadCluster, time.Duration)
}

func (c *clusterManager) Reconcile(ctx context.Context, cluster *workloadv1alpha1.WorkloadCluster) error {
	latestHeartbeat := time.Time{}
	for _, c := range cluster.Status.Conditions {
		if c.LastHeartbeatTime.Time.After(latestHeartbeat) {
			latestHeartbeat = c.LastHeartbeatTime.Time
		}
	}
	if latestHeartbeat.IsZero() {
		// Never seen a heartbeat; for now, this means the syncer hasn't heartbeated -- in the future, this should be un-Ready.
		return nil
	}

	if time.Since(latestHeartbeat) > c.heartbeatThreshold {
		conditions.MarkFalse(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityError,
			"No heartbeat since %s", latestHeartbeat)
	} else {
		conditions.MarkTrue(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition)

		// Enqueue another check after which the heartbeat should have been updated again.
		dur := time.Until(latestHeartbeat.Add(c.heartbeatThreshold))
		c.enqueueClusterAfter(cluster, dur)
	}

	return nil
}

func (c *clusterManager) Cleanup(ctx context.Context, deletedCluster *workloadv1alpha1.WorkloadCluster) {
}
