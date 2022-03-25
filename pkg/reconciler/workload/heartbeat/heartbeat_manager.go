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
	if cluster.Status.LastSyncerHeartbeatTime != nil {
		latestHeartbeat = cluster.Status.LastSyncerHeartbeatTime.Time
	}
	if latestHeartbeat.IsZero() {
		klog.V(5).Infof("Marking WorkloadCluster %s|%s not ready due to no heartbeat", cluster.ClusterName, cluster.Name)
		conditions.MarkFalse(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"No heartbeat yet seen")
	} else if time.Since(latestHeartbeat) > c.heartbeatThreshold {
		klog.V(5).Infof("Marking WorkloadCluster %s|%s not ready due to a stale heartbeat", cluster.ClusterName, cluster.Name)
		conditions.MarkFalse(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"No heartbeat since %s", latestHeartbeat)
	} else {
		klog.V(5).Infof("Marking WorkloadCluster %s|%s ready", cluster.ClusterName, cluster.Name)
		conditions.MarkTrue(cluster, workloadv1alpha1.WorkloadClusterReadyCondition)

		// Enqueue another check after which the heartbeat should have been updated again.
		dur := time.Until(latestHeartbeat.Add(c.heartbeatThreshold))
		c.enqueueClusterAfter(cluster, dur)
	}

	return nil
}

func (c *clusterManager) Cleanup(ctx context.Context, deletedCluster *workloadv1alpha1.WorkloadCluster) {
}
