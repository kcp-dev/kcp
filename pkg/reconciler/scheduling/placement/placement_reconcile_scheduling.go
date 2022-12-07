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
	"math/rand"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/kube-openapi/pkg/util/sets"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// placementReconciler watches namespaces within a cluster workspace and assigns those to location from
// the location domain of the cluster workspace.
type placementReconciler struct {
	listLocationsByPath func(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error)
}

func (r *placementReconciler) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error) {
	// get location workspace at first
	var locationWorkspace logicalcluster.Path
	if len(placement.Spec.LocationWorkspace) > 0 {
		locationWorkspace = logicalcluster.New(placement.Spec.LocationWorkspace)
	} else {
		locationWorkspace = logicalcluster.From(placement).Path()
	}

	validLocationNames, err := r.validLocationNames(placement, locationWorkspace)
	if err != nil {
		conditions.MarkFalse(placement, schedulingv1alpha1.PlacementReady, schedulingv1alpha1.LocationNotFoundReason, conditionsv1alpha1.ConditionSeverityError, err.Error())
		return reconcileStatusContinue, placement, err
	}

	switch placement.Status.Phase {
	case schedulingv1alpha1.PlacementBound:
		// if selected location becomes invalid when placement is in bound state, set PlacementReady
		// to false.
		if !isValidLocationSelected(placement, locationWorkspace, validLocationNames) {
			conditions.MarkFalse(
				placement,
				schedulingv1alpha1.PlacementReady,
				schedulingv1alpha1.LocationInvalidReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Selected location is invalid for current placement",
			)
			return reconcileStatusContinue, placement, nil
		}

		conditions.MarkTrue(placement, schedulingv1alpha1.PlacementReady)
		return reconcileStatusContinue, placement, nil
	case schedulingv1alpha1.PlacementUnbound:
		if isValidLocationSelected(placement, locationWorkspace, validLocationNames) {
			// if the selected location is valid, keep it.
			conditions.MarkTrue(placement, schedulingv1alpha1.PlacementReady)
			return reconcileStatusContinue, placement, nil
		}
	}

	// now it is pending state or in unbound state and needs a reselection
	if validLocationNames.Len() == 0 {
		placement.Status.Phase = schedulingv1alpha1.PlacementPending
		placement.Status.SelectedLocation = nil
		conditions.MarkFalse(
			placement,
			schedulingv1alpha1.PlacementReady,
			schedulingv1alpha1.LocationNotMatchReason,
			conditionsv1alpha1.ConditionSeverityError,
			"No valid location is found")
		return reconcileStatusContinue, placement, nil
	}

	candidates := make([]string, 0, validLocationNames.Len())
	for loc := range validLocationNames {
		candidates = append(candidates, loc)
	}

	// TODO(qiujian16): two placements could select the same location. We should
	// consider whether placements in a workspace should always select different locations.
	chosenLocation := candidates[rand.Intn(len(candidates))]
	placement.Status.SelectedLocation = &schedulingv1alpha1.LocationReference{
		Path:         locationWorkspace.String(),
		LocationName: chosenLocation,
	}
	placement.Status.Phase = schedulingv1alpha1.PlacementUnbound
	conditions.MarkTrue(placement, schedulingv1alpha1.PlacementReady)

	return reconcileStatusContinue, placement, nil
}

func (r *placementReconciler) validLocationNames(placement *schedulingv1alpha1.Placement, locationWorkspace logicalcluster.Path) (sets.String, error) {
	selectedLocations := sets.NewString()

	locations, err := r.listLocationsByPath(locationWorkspace)
	if err != nil {
		return selectedLocations, err
	}

	for _, loc := range locations {
		if loc.Spec.Resource != placement.Spec.LocationResource {
			continue
		}

		for _, s := range placement.Spec.LocationSelectors {
			selector, err := metav1.LabelSelectorAsSelector(&s)
			if err != nil {
				// skip this selector
				continue
			}

			if selector.Matches(labels.Set(loc.Labels)) {
				selectedLocations.Insert(loc.Name)
			}
		}
	}

	return selectedLocations, nil
}

func isValidLocationSelected(placement *schedulingv1alpha1.Placement, cluster logicalcluster.Path, validLocationNames sets.String) bool {
	if placement.Status.SelectedLocation == nil {
		return false
	}

	if placement.Status.SelectedLocation.Path != cluster.String() {
		return false
	}

	if !validLocationNames.Has(placement.Status.SelectedLocation.LocationName) {
		return false
	}

	return true
}
