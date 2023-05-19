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
	"context"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

const removingGracePeriod = 5 * time.Second

// reconcileScheduling reconciles the state.workload.kcp.io/<syncTarget> labels according the
// selected synctarget stored in the internal.workload.kcp.io/synctarget annotation
// on each placement.
func (c *controller) reconcileScheduling(
	ctx context.Context,
	_ string,
	ns *corev1.Namespace,
) (reconcileResult, error) {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.From(ns)

	validPlacements := []*schedulingv1alpha1.Placement{}
	_, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]

	if foundPlacement {
		placements, err := c.listPlacements(clusterName)
		if err != nil {
			return reconcileResult{}, err
		}

		validPlacements = filterValidPlacements(ns, placements)
	}

	// 1. pick all synctargets in all bound placements
	scheduledSyncTargets := sets.New[string]()
	for _, placement := range validPlacements {
		currentScheduled, foundScheduled := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]
		if !foundScheduled {
			continue
		}
		scheduledSyncTargets.Insert(currentScheduled)
	}

	// 2. find the scheduled synctarget to the ns, including synced, removing
	syncStatus := syncStatusFor(ns)

	// 3. if the synced synctarget is not in the scheduled synctargets, mark it as removing.
	changed := false
	annotations := ns.Annotations
	labels := ns.Labels

	for syncTarget := range syncStatus.active {
		if !scheduledSyncTargets.Has(syncTarget) {
			// it is no longer a synced synctarget, mark it as removing.
			now := c.now().UTC().Format(time.RFC3339)
			annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTarget] = now
			changed = true
			logger.WithValues("syncTarget", syncTarget).V(4).Info("setting SyncTarget as removing for Namespace since it is not a valid syncTarget anymore")
		}
	}

	// 4. remove the synctarget after grace period
	minEnqueueDuration := removingGracePeriod + 1
	for cluster, removingTime := range syncStatus.pendingRemoval {
		if removingTime.Add(removingGracePeriod).Before(c.now()) {
			delete(labels, workloadv1alpha1.ClusterResourceStateLabelPrefix+cluster)
			delete(annotations, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+cluster)
			changed = true
			logger.WithValues("syncTarget", cluster).V(4).Info("removing SyncTarget for Namespace")
		} else {
			enqueueDuration := time.Until(removingTime.Add(removingGracePeriod))
			if enqueueDuration < minEnqueueDuration {
				minEnqueueDuration = enqueueDuration
			}
		}
	}

	// 5. if a scheduled synctarget is not in synced and removing, add it in to the label
	for scheduledSyncTarget := range scheduledSyncTargets {
		if syncStatus.active.Has(scheduledSyncTarget) {
			continue
		}
		if _, ok := syncStatus.pendingRemoval[scheduledSyncTarget]; ok {
			continue
		}

		if labels == nil {
			labels = make(map[string]string)
			ns.Labels = labels
		}
		labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+scheduledSyncTarget] = string(workloadv1alpha1.ResourceStateSync)
		changed = true
		logger.WithValues("syncTarget", scheduledSyncTarget).V(4).Info("setting syncTarget as sync for Namespace")
	}

	// 6. Requeue at last to check if removing syncTarget should be removed later.
	var requeueAfter time.Duration
	if minEnqueueDuration <= removingGracePeriod {
		logger.WithValues("after", minEnqueueDuration).V(2).Info("enqueue Namespace later")
		requeueAfter = minEnqueueDuration
	}

	return reconcileResult{stop: changed, requeueAfter: requeueAfter}, nil
}

func syncStatusFor(ns *corev1.Namespace) namespaceSyncStatus {
	status := namespaceSyncStatus{
		active:         sets.New[string](),
		pendingRemoval: make(map[string]time.Time),
	}

	for k := range ns.Labels {
		if !strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			continue
		}

		syncTarget := strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix)

		deletionAnnotationKey := workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + syncTarget

		if value, ok := ns.Annotations[deletionAnnotationKey]; ok {
			removingTime, _ := time.Parse(time.RFC3339, value)
			status.pendingRemoval[syncTarget] = removingTime
			continue
		}

		status.active.Insert(syncTarget)
	}

	return status
}

type namespaceSyncStatus struct {
	active         sets.Set[string]
	pendingRemoval map[string]time.Time
}
