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

package cluster

import (
	"context"
	"time"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

var _ ClusterReconcileImpl = (*clusterManager)(nil)

type clusterManager struct {
	heartbeatThreshold time.Duration
}

func (c *clusterManager) Reconcile(ctx context.Context, cluster *workloadv1alpha1.WorkloadCluster) error {
	if cluster.Status.LastHeartbeat != nil && time.Since(cluster.Status.LastHeartbeat.Time) > c.heartbeatThreshold {
		conditions.MarkFalse(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition,
			workloadv1alpha1.ErrorHeartbeatMissedReason,
			conditionsv1alpha1.ConditionSeverityError,
			"No heartbeat since %s", cluster.Status.LastHeartbeat)
	} else {
		conditions.MarkTrue(cluster,
			workloadv1alpha1.WorkloadClusterReadyCondition)

		// TODO: enqueue another check in $heartbeatThreshold.
	}

	return nil
}

func (c *clusterManager) Cleanup(ctx context.Context, deletedCluster *workloadv1alpha1.WorkloadCluster) {
}
