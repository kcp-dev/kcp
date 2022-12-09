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
	"fmt"
	"math/rand"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	locationreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
)

// placementSchedulingReconciler schedules placments according to the selected locations.
// It considers only valid SyncTargets and updates the internal.workload.kcp.dev/synctarget
// annotation with the selected one on the placement object.
type placementSchedulingReconciler struct {
	listSyncTarget          func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)
	listWorkloadAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	getLocation             func(clusterName logicalcluster.Path, name string) (*schedulingv1alpha1.Location, error)
	patchPlacement          func(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*schedulingv1alpha1.Placement, error)
}

func (r *placementSchedulingReconciler) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error) {
	clusterName := logicalcluster.From(placement)

	// 1. get current scheduled
	expectedAnnotations := map[string]interface{}{} // nil means to remove the key
	currentScheduled, foundScheduled := placement.Annotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey]

	// 2. pick all valid synctargets in this placements
	validSyncTargets, reason, message, err := r.getAllValidSyncTargetsForPlacement(ctx, placement)
	if err != nil {
		return reconcileStatusStopAndRequeue, placement, err
	}

	// no valid synctarget, clean the annotation.
	if len(validSyncTargets) == 0 {
		if foundScheduled {
			expectedAnnotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] = nil
			updated, err := r.patchPlacementAnnotation(ctx, clusterName.Path(), placement, expectedAnnotations)
			return reconcileStatusContinue, updated, err
		}
		conditions.MarkFalse(placement, schedulingv1alpha1.PlacementScheduled, reason, conditionsv1alpha1.ConditionSeverityWarning, message)
		return reconcileStatusContinue, placement, nil
	}

	// 2. do nothing if scheduled cluster is in the valid clusters
	if foundScheduled {
		for _, syncTarget := range validSyncTargets {
			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.Name)
			if syncTargetKey != currentScheduled {
				continue
			}
			conditions.MarkTrue(placement, schedulingv1alpha1.PlacementScheduled)
			return reconcileStatusContinue, placement, nil
		}
	}

	// 3. randomly select one as the scheduled cluster
	// TODO(qiujian16): we currently schedule each in each location independently. It cannot guarantee 1 cluster is scheduled per location
	// when the same synctargets are in multiple locations, we need to rethink whether we need a better algorithm or we need location
	// to be exclusive.
	scheduledSyncTarget := validSyncTargets[rand.Intn(len(validSyncTargets))]
	expectedAnnotations[workloadv1alpha1.InternalSyncTargetPlacementAnnotationKey] = workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(scheduledSyncTarget), scheduledSyncTarget.Name)
	updated, err := r.patchPlacementAnnotation(ctx, clusterName.Path(), placement, expectedAnnotations)
	return reconcileStatusStopAndRequeue, updated, err
}

func (r *placementSchedulingReconciler) getAllValidSyncTargetsForPlacement(ctx context.Context, placement *schedulingv1alpha1.Placement) ([]*workloadv1alpha1.SyncTarget, string, string, error) {
	if placement.Status.Phase == schedulingv1alpha1.PlacementPending || placement.Status.SelectedLocation == nil {
		return nil, schedulingv1alpha1.ScheduleLocationNotFound, "No selected location is scheduled", nil
	}

	locationWorkspace := logicalcluster.NewPath(placement.Status.SelectedLocation.Path)
	location, err := r.getLocation(
		locationWorkspace,
		placement.Status.SelectedLocation.LocationName)
	switch {
	case errors.IsNotFound(err):
		return nil, schedulingv1alpha1.ScheduleLocationNotFound, "Selected location is not found", nil
	case err != nil:
		return nil, "", "", err
	}

	// find all synctargets in the location workspace
	syncTargets, err := r.listSyncTarget(logicalcluster.From(location))
	if err != nil {
		return nil, "", "", err
	}

	// filter the SyncTargets by location
	locationSyncTargets, err := locationreconciler.LocationSyncTargets(syncTargets, location)
	if len(locationSyncTargets) == 0 || err != nil {
		return nil, schedulingv1alpha1.ScheduleNoValidTargetReason, "No SyncTarget in the selected Location", err
	}

	// filter the SyncTargets by APIs
	validSyncTargets, message, err := r.filterAPICompatible(ctx, placement, locationSyncTargets)
	if len(validSyncTargets) == 0 || err != nil {
		return nil, schedulingv1alpha1.ScheduleNoValidTargetReason, message, err
	}

	// filter the SyncTargets by status.
	validSyncTargets = locationreconciler.FilterNonEvicting(locationreconciler.FilterReady(validSyncTargets))
	if len(validSyncTargets) == 0 {
		return validSyncTargets, schedulingv1alpha1.ScheduleNoValidTargetReason, "No SyncTarget is ready or non evicting", nil
	}

	return validSyncTargets, "", "", nil
}

func (r *placementSchedulingReconciler) filterAPICompatible(ctx context.Context, placement *schedulingv1alpha1.Placement, syncTargets []*workloadv1alpha1.SyncTarget) ([]*workloadv1alpha1.SyncTarget, string, error) {
	logger := klog.FromContext(ctx)
	var filteredSyncTargets []*workloadv1alpha1.SyncTarget

	apiBindings, err := r.listWorkloadAPIBindings(logicalcluster.From(placement))
	if err != nil {
		return filteredSyncTargets, "", err
	}

	var messages []string
	for _, syncTargert := range syncTargets {
		supportedAPIMap := map[apisv1alpha1.GroupResource]workloadv1alpha1.ResourceToSync{}
		for _, resource := range syncTargert.Status.SyncedResources {
			if resource.State == workloadv1alpha1.ResourceSchemaAcceptedState {
				supportedAPIMap[resource.GroupResource] = resource
			}
		}

		supported := true
		for _, binding := range apiBindings {
			for _, desiredAPI := range binding.Status.BoundResources {
				supportedAPI, ok := supportedAPIMap[apisv1alpha1.GroupResource{
					Group:    desiredAPI.Group,
					Resource: desiredAPI.Resource,
				}]
				if !ok || supportedAPI.IdentityHash != desiredAPI.Schema.IdentityHash {
					supported = false
					messages = append(messages, fmt.Sprintf("SyncTarget %s does not support APIBinding %s", syncTargert.Name, binding.Name))
					logger.V(4).Info("Does not support APIBindings", "workspace", logicalcluster.From(placement), "APIBinding", binding.Name, "syncTarget", syncTargert.Name)
					break
				}
			}
		}

		if supported {
			filteredSyncTargets = append(filteredSyncTargets, syncTargert)
		}
	}

	return filteredSyncTargets, strings.Join(messages, ", "), nil
}

func (r *placementSchedulingReconciler) patchPlacementAnnotation(ctx context.Context, clusterName logicalcluster.Path, placement *schedulingv1alpha1.Placement, annotations map[string]interface{}) (*schedulingv1alpha1.Placement, error) {
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
