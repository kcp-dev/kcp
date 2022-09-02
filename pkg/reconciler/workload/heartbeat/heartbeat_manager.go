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

	"k8s.io/klog/v2"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/basecontroller"
)

var _ basecontroller.ClusterReconcileImpl = (*clusterManager)(nil)

type clusterManager struct {
	heartbeatThreshold  time.Duration
	enqueueClusterAfter func(*workloadv1alpha1.SyncTarget, time.Duration)
}

func (c *clusterManager) Reconcile(ctx context.Context, cluster *workloadv1alpha1.SyncTarget) error {
	logger := klog.FromContext(ctx)
	defer conditions.SetSummary(
		cluster,
		conditions.WithConditions(
			workloadv1alpha1.SyncerReady,
			workloadv1alpha1.APIImporterReady,
			workloadv1alpha1.HeartbeatHealthy,
		),
	)

	latestHeartbeat := time.Time{}
	if cluster.Status.LastSyncerHeartbeatTime != nil {
		latestHeartbeat = cluster.Status.LastSyncerHeartbeatTime.Time
	}
	if latestHeartbeat.IsZero() {
		logger.V(5).Info("marking HeartbeatHealthy false for SyncTarget due to no heartbeat")
		conditions.MarkFalse(cluster,
			workloadv1alpha1.HeartbeatHealthy,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"No heartbeat yet seen")
	} else if time.Since(latestHeartbeat) > c.heartbeatThreshold {
		logger.V(5).Info("marking HeartbeatHealthy false for SyncTarget due to a stale heartbeat")
		conditions.MarkFalse(cluster,
			workloadv1alpha1.HeartbeatHealthy,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"No heartbeat since %s", latestHeartbeat)
	} else {
		logger.V(5).Info("marking Heartbeat healthy true for SyncTarget")
		conditions.MarkTrue(cluster, workloadv1alpha1.HeartbeatHealthy)

		// Enqueue another check after which the heartbeat should have been updated again.
		dur := time.Until(latestHeartbeat.Add(c.heartbeatThreshold))
		c.enqueueClusterAfter(cluster, dur)
	}

	return nil
}

func (c *clusterManager) Cleanup(ctx context.Context, deletedCluster *workloadv1alpha1.SyncTarget) {
}
