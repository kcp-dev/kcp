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
	"reflect"
	"sort"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	locationreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
	reconcilerapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

// placementReconciler watches namespaces within a cluster workspace and assigns those to location from
// the location domain of the cluster workspace.
type placementReconciler struct {
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	listLocations        func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error)
	listWorkloadClusters func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error)
	patchNamespace       func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)

	enqueueAfter func(logicalcluster.Name, *corev1.Namespace, time.Duration)
}

// TODO(sttts): split this up
// TODO(sttts): avoid recalculations with some cache
func (r *placementReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error) {
	clusterName := logicalcluster.From(ns)
	if !clusterName.HasPrefix(tenancyv1alpha1.RootCluster) {
		return reconcileStatusContinue, ns, nil
	}

	if value, found := ns.Labels[workloadv1alpha1.SchedulingDisabledLabel]; found && value == "true" {
		// ignore workload clusters that are disabled
		return reconcileStatusContinue, ns, nil
	}

	placementsValue, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]

	bindings, err := r.listAPIBindings(clusterName)
	if err != nil {
		return reconcileStatusStop, nil, err
	}
	deletePlacementAnnotation := func() (reconcileStatus, *corev1.Namespace, error) {
		var deletePlacementAnnotation bool
		var deleteNegotationWorkspaceAnnotation bool

		if _, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]; found {
			deletePlacementAnnotation = true
		}
		if _, found := ns.Annotations[schedulingv1alpha1.InternalNegotiationWorkspaceAnnotationKey]; found {
			deleteNegotationWorkspaceAnnotation = true
		}
		if deletePlacementAnnotation || deleteNegotationWorkspaceAnnotation {
			klog.V(4).Infof("Removing placement from namespace %s|%s, no api bindings", ns.Name)

			oldData, err := json.Marshal(&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: ns.Annotations,
				},
			})
			if err != nil {
				return reconcileStatusStop, nil, err
			}

			newAnnotations := map[string]string{}
			for k, v := range ns.Annotations {
				if k == schedulingv1alpha1.PlacementAnnotationKey && deletePlacementAnnotation {
					continue
				}

				if k == schedulingv1alpha1.InternalNegotiationWorkspaceAnnotationKey && deleteNegotationWorkspaceAnnotation {
					continue
				}

				newAnnotations[k] = v
			}

			newData, err := json.Marshal(&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Annotations:     newAnnotations,
					UID:             ns.UID,
					ResourceVersion: ns.ResourceVersion,
				},
			})
			if err != nil {
				return reconcileStatusStop, nil, err
			}
			patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
			if err != nil {
				return reconcileStatusStop, nil, err
			}

			if _, err := r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
				return reconcileStatusStop, nil, err
			}
		}
		return reconcileStatusContinue, ns, nil
	}
	if len(bindings) == 0 {
		if foundPlacement {
			return deletePlacementAnnotation()
		}
		return reconcileStatusContinue, ns, nil
	}

	// find workload bindings = those that have at least one WorkloadCluster or Location
	var errs []error
	var workloadBindings []*apisv1alpha1.APIBinding
	for _, binding := range bindings {
		if !conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted) || !conditions.IsTrue(binding, apisv1alpha1.APIExportValid) {
			continue
		}
		if binding.Spec.Reference.Workspace == nil {
			continue
		}
		if binding.Spec.Reference.Workspace.ExportName != reconcilerapiexport.TemporaryComputeServiceExportName {
			continue
		}
		negotationClusterName := logicalcluster.New(binding.Spec.Reference.Workspace.Path)
		clusters, err := r.listWorkloadClusters(negotationClusterName)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		locations, err := r.listLocations(negotationClusterName)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(clusters) > 0 || len(locations) > 0 {
			workloadBindings = append(workloadBindings, binding)
		}
	}
	if len(workloadBindings) == 0 && len(errs) > 0 {
		return reconcileStatusStop, nil, utilserrors.NewAggregate(errs)
	}
	if len(workloadBindings) == 0 {
		if ret, ns, err := deletePlacementAnnotation(); err != nil {
			return ret, ns, err
		}

		// TODO(sttts): we have no good way to identify workload APIBinding right now. So we use some quite high
		//              requeue duration. But better than not requeueing at all.
		r.enqueueAfter(clusterName, ns, time.Minute*2)
		return reconcileStatusContinue, ns, nil
	}

	// sort found bindings, take the first one, just to have stability.
	// TODO(sttts): somehow avoid the situation of multiple workload bindings. Admissions?
	sort.Slice(workloadBindings, func(i, j int) bool {
		return workloadBindings[i].Name < workloadBindings[j].Name
	})
	binding := workloadBindings[0]
	negotiationClusterName := logicalcluster.New(binding.Spec.Reference.Workspace.Path)

	locations, err := r.listLocations(negotiationClusterName)
	if err != nil {
		return reconcileStatusStop, nil, err
	}

	// fake default location, if there is none. It will match every WorkloadCluster.
	if len(locations) == 0 {
		locations = []*schedulingv1alpha1.Location{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
				Spec: schedulingv1alpha1.LocationSpec{
					InstanceSelector: &metav1.LabelSelector{}, // matches everything
				},
			},
		}
	}

	workloadClusterNames := sets.NewString()
	workloadClusters, err := r.listWorkloadClusters(negotiationClusterName)
	if err != nil {
		klog.Errorf("failed to list WorkloadClusters in %s for APIBinding %s|%s: %v", negotiationClusterName, clusterName, binding.Name, err)
		return reconcileStatusStop, nil, err
	}
	for _, workloadCluster := range workloadClusters {
		workloadClusterNames.Insert(workloadCluster.Name)
	}

	// find clusters per locations
	var lastErr error
	admissiblePlacements := sets.NewString()
	readyPlacements := sets.NewString()
	locationsByName := map[string]*schedulingv1alpha1.Location{}
	for _, l := range locations {
		locationsByName[l.Name] = l

		locationClusters, err := locationreconciler.LocationWorkloadClusters(workloadClusters, l)
		if err != nil {
			lastErr = fmt.Errorf("failed to get location %s|%s WorkloadClusters: %w", negotiationClusterName, l.Name, err)
			continue // try another one
		}

		for _, wc := range locationClusters {
			placement := placementString(negotiationClusterName, l.Name, wc.Name)
			// TODO(sttts): add migration when a cluster is unready for n minutes

			if conditions.IsTrue(wc, conditionsapi.ReadyCondition) && !wc.Spec.Unschedulable {
				readyPlacements.Insert(placement)
			}

			if wc.Spec.EvictAfter == nil || time.Now().Before(wc.Spec.EvictAfter.Time) {
				admissiblePlacements.Insert(placement)
			}
		}
	}

	// go through existing placement and potentially evict some
	newPlacements := schedulingv1alpha1.PlacementAnnotation{}
	removedPlacementsByWorkspaceCluster := map[string]string{}
	var oldPlacements schedulingv1alpha1.PlacementAnnotation
	if foundPlacement {
		if err := json.Unmarshal([]byte(placementsValue), &oldPlacements); err != nil {
			klog.Errorf("failed to unmarshal placement annotation %q of namespace %s|%s: %v", placementsValue, clusterName, ns.Name, err)
			oldPlacements = schedulingv1alpha1.PlacementAnnotation{} // assume no placement
		}
		for placement, state := range oldPlacements {
			placementClusterName, _, workloadCluster, ok := ParsePlacementString(placement)
			if !ok {
				klog.Errorf("failed to parse placement %q for namespace %s|%s", placement, clusterName, ns.Name)
				continue
			}
			if placementClusterName != negotiationClusterName {
				klog.V(3).Infof("dropping placement %q for namespace %s|%s because it is not in the negotiation cluster %q", placement, clusterName, ns.Name, negotiationClusterName)
				continue
			}

			switch state {
			case schedulingv1alpha1.PlacementStatePending:
				if admissiblePlacements.Has(placement) {
					newPlacements[placement] = state
				} else {
					klog.V(3).Infof("dropping placement %q for namespace %s|%s because it is not admissible", placement, clusterName, ns.Name)
				}
			case schedulingv1alpha1.PlacementStateBound:
				if admissiblePlacements.Has(placement) {
					newPlacements[placement] = state
				} else if workloadClusterNames.Has(workloadCluster) {
					newPlacements[placement] = schedulingv1alpha1.PlacementStateRemoving

					// remember that we set this placement to Removing. We might undo it later under a different location.
					removedPlacementsByWorkspaceCluster[workloadCluster] = placement
				} else {
					klog.V(3).Infof("dropping placement %q for namespace %s|%s because the workload cluster %q is gone", placement, clusterName, ns.Name, workloadCluster)
				}
			case schedulingv1alpha1.PlacementStateRemoving:
				if workloadClusterNames.Has(workloadCluster) {
					// TODO(sttts): add some timeout when we remove the hard way
					newPlacements[placement] = state
				} else {
					klog.V(3).Infof("dropping placement %q for namespace %s|%s because the workload cluster %q is gone", placement, clusterName, ns.Name, workloadCluster)
				}
			case schedulingv1alpha1.PlacementStateUnbound:
			}
		}
	}

	// go through new placements and see which we have to reschedule
	activeLocations := sets.NewString() // pending or bound
	locationsToReschedule := sets.NewString()
	placedWorkloadClusters := sets.NewString()
	for placement, state := range newPlacements {
		_, locationName, workloadClusterName, ok := ParsePlacementString(placement)
		if !ok {
			klog.Errorf("failed to parse placement %q for namespace %s|%s", placement, clusterName, ns.Name)
			continue
		}

		placedWorkloadClusters.Insert(workloadClusterName)

		switch state {
		case schedulingv1alpha1.PlacementStatePending, schedulingv1alpha1.PlacementStateBound:
			activeLocations.Insert(locationName)
			locationsToReschedule.Delete(locationName)
		case schedulingv1alpha1.PlacementStateRemoving:
			if _, found := locationsByName[locationName]; found && !activeLocations.Has(locationName) {
				locationsToReschedule.Insert(locationName)
			}
		}
	}

	// (re)schedule
	if len(locationsToReschedule) == 0 && len(activeLocations) > 0 {
		return reconcileStatusContinue, ns, nil
	} else if len(locationsToReschedule) == 0 && len(activeLocations) == 0 {
		// choose a location, and a cluster in that location
		perm := rand.Perm(len(locations))
		var chosenPlacement string
		var chosenState schedulingv1alpha1.PlacementState
		for i := range locations {
			location := locations[perm[i]]
			locationClusters, err := locationreconciler.LocationWorkloadClusters(workloadClusters, location)
			if err != nil {
				klog.Errorf("failed to get workload clusters for location %|%s: %v", negotiationClusterName, location.Name, err)
				continue // try another one
			}

			// attempt to place in this location
			chosenCluster, state, success := place(negotiationClusterName, location, locationClusters, placedWorkloadClusters, removedPlacementsByWorkspaceCluster)
			if !success {
				continue
			}
			chosenPlacement = placementString(negotiationClusterName, location.Name, chosenCluster.Name)
			chosenState = state
			if toBeRemovedPlacement, found := removedPlacementsByWorkspaceCluster[chosenCluster.Name]; found {
				klog.V(3).Infof("Reviving removed placement %q for namespace %s|%s under different location %s", toBeRemovedPlacement, clusterName, ns.Name, location.Name)
				delete(newPlacements, toBeRemovedPlacement)
				delete(removedPlacementsByWorkspaceCluster, chosenCluster.Name)
				break // we found a location of a cluster we are already scheduled to
			}

			// do not break here, but keep looking for a potential toBeRemovedPlacement case
		}
		if chosenPlacement == "" {
			// TODO(sttts): come up with some both quicker rescheduling initially, but also some backoff when scheduling fails again
			klog.V(3).Infof("Requeuing after 2m, failed to schedule Namespace %s|%s against locations in %s. No ready clusters: %v", clusterName, ns.Name, negotiationClusterName, lastErr)
			r.enqueueAfter(clusterName, ns, time.Minute*2)
		} else {
			klog.V(3).Infof("Placing namespace %s|%s on %s=%q", clusterName, ns.Name, chosenPlacement, chosenState)
			newPlacements[chosenPlacement] = chosenState
		}
	} else if len(locationsToReschedule) > 0 {
		// reschedule within one or more locations
		for locationName := range locationsToReschedule {
			location := locationsByName[locationName] // when we are here, the location definitely exists
			locationClusters, err := locationreconciler.LocationWorkloadClusters(workloadClusters, location)
			if err != nil {
				klog.Errorf("failed to get workload clusters for location %|%s: %v", negotiationClusterName, locationName, err)
				continue // try another one
			}

			// attempt re-place in this location
			chosenCluster, state, success := place(negotiationClusterName, location, locationClusters, placedWorkloadClusters, removedPlacementsByWorkspaceCluster)
			if !success {
				klog.V(3).Infof("Requeuing after 2m, failed to schedule Namespace %s|%s against locations in %s. No ready clusters: %v", clusterName, ns.Name, negotiationClusterName, lastErr)
				// TODO(sttts): come up with some both quicker rescheduling initially, but also some backoff when scheduling fails again
				r.enqueueAfter(clusterName, ns, time.Minute*2)
				continue // nothing we can do with this location
			}
			if toBeRemovedPlacement, found := removedPlacementsByWorkspaceCluster[chosenCluster.Name]; found {
				klog.V(3).Infof("Reviving removed placement %q for namespace %s|%s under different location %s", toBeRemovedPlacement, clusterName, ns.Name, location.Name)
				delete(newPlacements, toBeRemovedPlacement)
				delete(removedPlacementsByWorkspaceCluster, chosenCluster.Name)
				break // we found a location of a cluster we are already scheduled to
			}

			placedWorkloadClusters.Insert(chosenCluster.Name)
			placement := placementString(negotiationClusterName, location.Name, chosenCluster.Name)
			klog.V(3).Infof("Placing namespace %s|%s on %s=%q", clusterName, ns.Name, placement, state)
			newPlacements[placement] = state
		}
	}

	// anything changed?
	if reflect.DeepEqual(oldPlacements, newPlacements) {
		return reconcileStatusContinue, ns, nil
	}

	// patch Namespace
	bs, err := json.Marshal(newPlacements)
	if err != nil {
		klog.Errorf("failed to marshal placement %v for namespace %s|%s: %v", newPlacements, clusterName, ns.Name, err)
		return reconcileStatusStop, nil, err
	}
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"resourceVersion": ns.ResourceVersion,
			"uid":             ns.UID,
		},
	}
	if err := unstructured.SetNestedField(patch, string(bs), "metadata", "annotations", schedulingv1alpha1.PlacementAnnotationKey); err != nil {
		klog.Errorf("failed to set placement annotation for namespace %s|%s: %v", clusterName, ns.Name, err)
		return reconcileStatusStop, nil, err
	}
	if err := unstructured.SetNestedField(patch, negotiationClusterName.String(), "metadata", "annotations", schedulingv1alpha1.InternalNegotiationWorkspaceAnnotationKey); err != nil {
		klog.Errorf("failed to set negotiation cluster annotation for namespace %s|%s: %v", clusterName, ns.Name, err)
		return reconcileStatusStop, nil, err
	}
	bs, err = json.Marshal(patch)
	if err != nil {
		klog.Errorf("failed to marshal patch %v against namespace %s|%s: %v", patch, clusterName, ns.Name, err)
		return reconcileStatusStop, nil, err
	}
	if updated, err := r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, bs, metav1.PatchOptions{}); err != nil {
		return reconcileStatusStop, ns, err
	} else {
		return reconcileStatusContinue, updated, nil
	}
}

func place(negotiationClusterName logicalcluster.Name, location *schedulingv1alpha1.Location, workloadClusters []*workloadv1alpha1.WorkloadCluster, forbidden sets.String, toRevive map[string]string) (workloadCluster *workloadv1alpha1.WorkloadCluster, state schedulingv1alpha1.PlacementState, success bool) {
	locationClusters, err := locationreconciler.LocationWorkloadClusters(workloadClusters, location)
	if err != nil {
		klog.Errorf("failed to get workload clusters for location %|%s: %v", negotiationClusterName, location.Name, err)
		return nil, "", false
	}
	possibleClusters := locationreconciler.FilterNonEvicting(locationreconciler.FilterReady(locationClusters))
	if len(possibleClusters) == 0 {
		return nil, "", false
	}

	// filter out forbidden clusters, and prefer preferred clusters
	state = schedulingv1alpha1.PlacementStatePending // by default the cluster is fresh
	candidates := make([]*workloadv1alpha1.WorkloadCluster, 0, len(possibleClusters))
	for _, cluster := range possibleClusters {
		if _, found := toRevive[cluster.Name]; found {
			// We have just set this to Removing, but not committed that yet. Revive it.
			candidates = []*workloadv1alpha1.WorkloadCluster{cluster}
			state = schedulingv1alpha1.PlacementStateBound
			break
		} else if !forbidden.Has(cluster.Name) {
			candidates = append(candidates, cluster)
		}
	}

	// TODO(sttts): be more clever than just random: follow allocable, co-location workspace and workloads, load-balance, etcd.
	chosenCluster := candidates[rand.Intn(len(candidates))]
	return chosenCluster, state, true
}

func placementString(cluster logicalcluster.Name, location string, workloadCluster string) string {
	return fmt.Sprintf("%s+%s+%s", cluster, location, workloadCluster)
}

func ParsePlacementString(placement string) (cluster logicalcluster.Name, location string, workloadCluster string, valid bool) {
	parts := strings.Split(placement, "+")
	if len(parts) != 3 {
		return logicalcluster.Name{}, "", "", false
	}
	return logicalcluster.New(parts[0]), parts[1], parts[2], true
}
