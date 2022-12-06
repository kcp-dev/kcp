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
	"encoding/json"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const removingGracePeriod = 5 * time.Second

// placementSchedulingReconciler reconciles the state.workload.kcp.dev/<syncTarget> labels according the
// selected synctarget stored in the internal.workload.kcp.dev/synctarget annotation
// on each placement.
type placementSchedulingReconciler struct {
	listPlacement func(clusterName logicalcluster.Path) ([]*schedulingv1alpha1.Placement, error)

	patchNamespace func(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)

	enqueueAfter func(*corev1.Namespace, time.Duration)

	now func() time.Time
}

func (r *placementSchedulingReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error) {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.From(ns)

	validPlacements := []*schedulingv1alpha1.Placement{}
	_, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]

	if foundPlacement {
		placements, err := r.listPlacement(clusterName)
		if err != nil {
			return reconcileStatusStop, ns, err
		}

		validPlacements = filterValidPlacements(ns, placements)
	}

	// 1. pick all synctargets in all bound placements
	scheduledSyncTargets := sets.NewString()
	for _, placement := range validPlacements {
		currentScheduled, foundScheduled := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]
		if !foundScheduled {
			continue
		}
		scheduledSyncTargets.Insert(currentScheduled)
	}

	// 2. find the scheduled synctarget to the ns, including synced, removing
	synced, removing := syncedRemovingCluster(ns)

	// 3. if the synced synctarget is not in the scheduled synctargets, mark it as removing.
	expectedAnnotations := map[string]interface{}{} // nil means to remove the key
	expectedLabels := map[string]interface{}{}      // nil means to remove the key

	for syncTarget := range synced {
		if !scheduledSyncTargets.Has(syncTarget) {
			// it is no longer a synced synctarget, mark it as removing.
			now := r.now().UTC().Format(time.RFC3339)
			expectedAnnotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTarget] = now
			logger.WithValues("syncTarget", syncTarget).V(4).Info("setting SyncTarget as removing for Namespace since it is not a valid syncTarget anymore")
		}
	}

	// 4. remove the synctarget after grace period
	minEnqueueDuration := removingGracePeriod + 1
	for cluster, removingTime := range removing {
		if removingTime.Add(removingGracePeriod).Before(r.now()) {
			expectedLabels[workloadv1alpha1.ClusterResourceStateLabelPrefix+cluster] = nil
			expectedAnnotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+cluster] = nil
			logger.WithValues("syncTarget", cluster).V(4).Info("removing SyncTarget for Namespace")
		} else {
			enqueuDuration := time.Until(removingTime.Add(removingGracePeriod))
			if enqueuDuration < minEnqueueDuration {
				minEnqueueDuration = enqueuDuration
			}
		}
	}

	// 5. if a scheduled synctarget is not in synced and removing, add it in to the label
	for scheduledSyncTarget := range scheduledSyncTargets {
		if synced.Has(scheduledSyncTarget) {
			continue
		}
		if _, ok := removing[scheduledSyncTarget]; ok {
			continue
		}

		expectedLabels[workloadv1alpha1.ClusterResourceStateLabelPrefix+scheduledSyncTarget] = string(workloadv1alpha1.ResourceStateSync)
		logger.WithValues("syncTarget", scheduledSyncTarget).V(4).Info("setting syncTarget as sync for Namespace")
	}

	if len(expectedLabels) > 0 || len(expectedAnnotations) > 0 {
		ns, err := r.patchNamespaceLabelAnnotation(ctx, clusterName, ns, expectedLabels, expectedAnnotations)
		return reconcileStatusContinue, ns, err
	}

	// 6. Requeue at last to check if removing syncTarget should be removed later.
	if minEnqueueDuration <= removingGracePeriod {
		logger.WithValues("after", minEnqueueDuration).V(2).Info("enqueue Namespace later")
		r.enqueueAfter(ns, minEnqueueDuration)
	}

	return reconcileStatusContinue, ns, nil
}

func (r *placementSchedulingReconciler) patchNamespaceLabelAnnotation(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace, labels, annotations map[string]interface{}) (*corev1.Namespace, error) {
	logger := klog.FromContext(ctx)
	patch := map[string]interface{}{}
	if len(annotations) > 0 {
		if err := unstructured.SetNestedField(patch, annotations, "metadata", "annotations"); err != nil {
			return ns, err
		}
	}
	if len(labels) > 0 {
		if err := unstructured.SetNestedField(patch, labels, "metadata", "labels"); err != nil {
			return ns, err
		}
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return ns, err
	}
	logger.WithValues("patch", string(patchBytes)).V(3).Info("patching Namespace to update SyncTarget information")
	updated, err := r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return ns, err
	}
	return updated, nil
}

// syncedRemovingCluster finds synced and removing clusters for this ns.
func syncedRemovingCluster(ns *corev1.Namespace) (sets.String, map[string]time.Time) {
	synced := sets.NewString()
	removing := map[string]time.Time{}
	for k := range ns.Labels {
		if !strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			continue
		}

		syncTarget := strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix)

		deletionAnnotationKey := workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + syncTarget

		if value, ok := ns.Annotations[deletionAnnotationKey]; ok {
			removingTime, _ := time.Parse(time.RFC3339, value)
			removing[syncTarget] = removingTime
			continue
		}

		synced.Insert(syncTarget)
	}

	return synced, removing
}
