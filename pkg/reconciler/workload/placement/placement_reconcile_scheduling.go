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

package placement

import (
	"context"
	"encoding/json"
	"math/rand"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	locationreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
)

// placementSchedulingReconciler schedules placments according to the selected locations.
// It considers only valid SyncTargets and updates the internal.workload.kcp.dev/synctarget
// annotation with the selected one on the placement object.
type placementSchedulingReconciler struct {
	listSyncTarget func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)
	getLocation    func(clusterName logicalcluster.Name, name string) (*schedulingv1alpha1.Location, error)
	patchPlacement func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*schedulingv1alpha1.Placement, error)
}

func (r *placementSchedulingReconciler) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error) {
	clusterName := logicalcluster.From(placement)

	// 1. get current scheduled
	expectedAnnotations := map[string]interface{}{} // nil means to remove the key
	currentScheduled, foundScheduled := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]

	// 2. pick all valid synctargets in this placements
	syncTargetClusterName, syncTargets, err := r.getAllValidSyncTargetsForPlacement(clusterName, placement)
	if err != nil {
		return reconcileStatusStop, placement, err
	}

	// no valid synctarget, clean the annotation.
	if foundScheduled && len(syncTargets) == 0 {
		expectedAnnotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] = nil
		updated, err := r.patchPlacementAnnotation(ctx, clusterName, placement, expectedAnnotations)
		return reconcileStatusContinue, updated, err
	}

	// 2. do nothing if scheduled cluster is in the valid clusters
	if foundScheduled && len(syncTargets) > 0 {
		for _, syncTarget := range syncTargets {
			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.Name)
			if syncTargetKey != currentScheduled {
				continue
			}
			return reconcileStatusContinue, placement, nil
		}
	}

	// 3. randomly select one as the scheduled cluster
	// TODO(qiujian16): we currently schedule each in each location independently. It cannot guarantee 1 cluster is scheduled per location
	// when the same synctargets are in multiple locations, we need to rethink whether we need a better algorithm or we need location
	// to be exclusive.
	if len(syncTargets) > 0 {
		scheduledSyncTarget := syncTargets[rand.Intn(len(syncTargets))]
		expectedAnnotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] = workloadv1alpha1.ToSyncTargetKey(syncTargetClusterName, scheduledSyncTarget.Name)
		updated, err := r.patchPlacementAnnotation(ctx, clusterName, placement, expectedAnnotations)
		return reconcileStatusContinue, updated, err
	}

	return reconcileStatusContinue, placement, nil
}

func (r *placementSchedulingReconciler) getAllValidSyncTargetsForPlacement(clusterName logicalcluster.Name, placement *schedulingv1alpha1.Placement) (logicalcluster.Name, []*workloadv1alpha1.SyncTarget, error) {
	if placement.Status.Phase == schedulingv1alpha1.PlacementPending || placement.Status.SelectedLocation == nil {
		return logicalcluster.Name{}, nil, nil
	}

	locationWorkspace := logicalcluster.New(placement.Status.SelectedLocation.Path)
	location, err := r.getLocation(
		locationWorkspace,
		placement.Status.SelectedLocation.LocationName)
	switch {
	case errors.IsNotFound(err):
		return locationWorkspace, nil, nil
	case err != nil:
		return locationWorkspace, nil, err
	}

	// find all synctargets in the location workspace
	syncTargets, err := r.listSyncTarget(locationWorkspace)
	if err != nil {
		return locationWorkspace, nil, err
	}

	// filter the sync targets by location
	locationClusters, err := locationreconciler.LocationSyncTargets(syncTargets, location)
	if err != nil {
		return locationWorkspace, nil, err
	}

	// find all the valid sync targets.
	validClusters := locationreconciler.FilterNonEvicting(locationreconciler.FilterReady(locationClusters))

	return locationWorkspace, validClusters, nil
}

func (r *placementSchedulingReconciler) patchPlacementAnnotation(ctx context.Context, clusterName logicalcluster.Name, placement *schedulingv1alpha1.Placement, annotations map[string]interface{}) (*schedulingv1alpha1.Placement, error) {
	logger := klog.FromContext(ctx)
	patch := map[string]interface{}{}
	if len(annotations) > 0 {
		if err := unstructured.SetNestedField(patch, annotations, "metadata", "annotations"); err != nil {
			return placement, err
		}
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return placement, err
	}
	logger.WithValues("patch", string(patchBytes)).V(3).Info("patching Placement to update SyncTarget information")
	updated, err := r.patchPlacement(ctx, clusterName, placement.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return placement, err
	}
	return updated, nil
}
